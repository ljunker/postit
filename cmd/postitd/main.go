// Command postitd is the sync service for p.
//
// Configuration via environment:
//
//	POSTIT_TOKEN  shared secret clients send as Bearer token (required, ≥ 32 chars)
//	POSTIT_DB     database path (default /data/postit.db)
//	POSTIT_ADDR   listen address (default :8080)
//
// "postitd healthcheck" asks the running service for /healthz and exits 0 if
// it is healthy (for Docker's HEALTHCHECK; the image has no curl).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"postit/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("postitd", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	token := os.Getenv("POSTIT_TOKEN")
	if len(token) < 32 {
		return errors.New("POSTIT_TOKEN must be set to at least 32 characters (e.g. openssl rand -hex 32)")
	}
	dbPath := envOr("POSTIT_DB", "/data/postit.db")
	addr := envOr("POSTIT_ADDR", ":8080")

	db, err := server.OpenDB(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	srv := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(db, token, log),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "db", dbPath)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
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
