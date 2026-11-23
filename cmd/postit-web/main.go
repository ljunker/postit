// Command postit-web is the browser UI for p. It keeps its own replica of the
// notes and syncs it with postitd like any other device.
//
// Configuration via environment:
//
//	POSTIT_TOKEN         sync token, the same as postitd's (required, ≥ 32 chars)
//	POSTIT_WEB_PASSWORD  login password for the browser (required, ≥ 12 chars)
//	POSTIT_SERVER        postitd base URL (default http://postitd:8080)
//	POSTIT_WEB_DB        database path (default /data/web.db)
//	POSTIT_ADDR          listen address (default :8080)
//
// "postit-web healthcheck" asks the running service for /healthz and exits 0
// if it is healthy (for Docker's HEALTHCHECK; the image has no curl).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"postit/store"
	"postit/syncclient"
	"postit/web"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("postit-web", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	token := os.Getenv("POSTIT_TOKEN")
	if len(token) < 32 {
		return errors.New("POSTIT_TOKEN must be set to postitd's token (at least 32 characters)")
	}
	password := os.Getenv("POSTIT_WEB_PASSWORD")
	if len(password) < 12 {
		return errors.New("POSTIT_WEB_PASSWORD must be set to at least 12 characters")
	}
	server := strings.TrimRight(envOr("POSTIT_SERVER", "http://postitd:8080"), "/")
	dbPath := envOr("POSTIT_WEB_DB", "/data/web.db")
	addr := envOr("POSTIT_ADDR", ":8080")

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	syncer := web.NewSyncer(st, syncclient.New(syncclient.Config{Server: server, Token: token}), log)
	app := web.New(st, syncer, password, token, log)

	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	syncCtx, stopSync := context.WithCancel(context.Background())
	syncDone := make(chan struct{})
	go func() {
		defer close(syncDone)
		syncer.Run(syncCtx)
	}()

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dbPath, "server", server)
		errc <- srv.ListenAndServe()
	}()

	var runErr error
	select {
	case runErr = <-errc:
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = err
	}
	stopSync()
	<-syncDone
	// Push the last changes before the replica goes away.
	finalCtx, cancelFinal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinal()
	syncer.SyncNow(finalCtx)
	return runErr
}

func healthcheck() int {
	_, port, err := net.SplitHostPort(envOr("POSTIT_ADDR", ":8080"))
	if err != nil {
		return 1
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
