package web

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"postit/store"
	"postit/syncclient"
)

// Syncer keeps the web app's replica in step with postitd, like p does on a
// device: on start, periodically, shortly after every local change, and
// before a page is shown if the last attempt is too old.
type Syncer struct {
	st       *store.Store
	client   *syncclient.Client
	log      *slog.Logger
	Interval time.Duration // background sync
	Debounce time.Duration // after a local change
	Timeout  time.Duration // per background sync

	sem  chan struct{} // one sync at a time; acquiring honours a context
	kick chan struct{}

	mu        sync.Mutex
	attempted time.Time
	ok        time.Time
	err       error
}

func NewSyncer(st *store.Store, client *syncclient.Client, log *slog.Logger) *Syncer {
	return &Syncer{
		st: st, client: client, log: log,
		Interval: 15 * time.Second,
		Debounce: time.Second,
		Timeout:  15 * time.Second,
		sem:      make(chan struct{}, 1),
		kick:     make(chan struct{}, 1),
	}
}

// Run syncs until ctx ends.
func (s *Syncer) Run(ctx context.Context) {
	s.background(ctx)
	tick := time.NewTicker(s.Interval)
	defer tick.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.background(ctx)
		case <-s.kick:
			debounce = time.After(s.Debounce)
		case <-debounce:
			debounce = nil
			s.background(ctx)
		}
	}
}

func (s *Syncer) background(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	s.SyncNow(ctx)
}

// Changed schedules a sync shortly after a local change.
func (s *Syncer) Changed() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// SyncNow runs one sync, waiting for a running one to finish first.
func (s *Syncer) SyncNow(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.sem }()

	res, err := s.client.Sync(ctx, s.st)
	s.mu.Lock()
	s.attempted = time.Now()
	if err == nil {
		s.ok = s.attempted
	}
	prev := s.err
	s.err = err
	s.mu.Unlock()

	switch {
	case err != nil && (prev == nil || prev.Error() != err.Error()):
		s.log.Warn("sync failed", "err", err)
	case err == nil && prev != nil:
		s.log.Info("sync recovered")
	}
	if err == nil && (res.Pushed > 0 || res.Pulled > 0) {
		s.log.Info("sync", "pushed", res.Pushed, "pulled", res.Pulled)
	}
	return err
}

// Fresh syncs if the last attempt is older than maxAge, waiting at most wait.
// It is called before a page is rendered so the page shows current data.
func (s *Syncer) Fresh(ctx context.Context, maxAge, wait time.Duration) {
	s.mu.Lock()
	stale := time.Since(s.attempted) > maxAge
	s.mu.Unlock()
	if !stale {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	s.SyncNow(ctx)
}

// Status reports the time of the last successful sync and the last error.
func (s *Syncer) Status() (ok time.Time, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ok, s.err
}
