package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"postit/syncapi"
)

// maxNote limits the body of a post-it filed via the inbox endpoint.
const maxNote = 64 << 10

// maxBody limits a sync request (all notes of a fresh device fit easily).
const maxBody = 16 << 20

// Handler returns the service's routes. token is the shared secret every
// request to /api/ must send as "Authorization: Bearer <token>".
func Handler(db *DB, token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	api := http.NewServeMux()
	api.HandleFunc("GET "+syncapi.PingPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("POST "+syncapi.Path, func(w http.ResponseWriter, r *http.Request) {
		var req syncapi.Request
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
			return
		}
		if err := validate(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := db.Sync(req)
		if err != nil {
			log.Error("sync", "err", err)
			writeError(w, http.StatusInternalServerError, "sync failed")
			return
		}
		log.Info("sync", "since", req.Since, "pushed", len(req.Projects)+len(req.Notes),
			"pulled", len(resp.Projects)+len(resp.Notes), "cursor", resp.Cursor)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	api.HandleFunc("POST "+syncapi.InboxPath, func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNote))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "note too long")
			return
		}
		body := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if body == "" {
			writeError(w, http.StatusBadRequest, "empty note")
			return
		}
		t := time.Now().UnixNano()
		note := syncapi.Note{
			UID:        uuid.NewString(),
			ProjectUID: syncapi.InboxUID,
			Body:       body,
			Color:      "yellow",
			CreatedAt:  t,
			UpdatedAt:  t,
			ModifiedAt: t,
		}
		// Since = MaxInt64: push only, pull nothing back.
		if _, err := db.Sync(syncapi.Request{Since: math.MaxInt64, Notes: []syncapi.Note{note}}); err != nil {
			log.Error("inbox", "err", err)
			writeError(w, http.StatusInternalServerError, "saving failed")
			return
		}
		log.Info("inbox", "uid", note.UID, "len", len(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(note)
	})
	mux.Handle("/api/", requireToken(token, api))

	return logRequests(log, mux)
}

func validate(req syncapi.Request) error {
	if req.Since < 0 {
		return errors.New("since must not be negative")
	}
	for _, p := range req.Projects {
		if p.UID == "" {
			return errors.New("project without uid")
		}
	}
	for _, n := range req.Notes {
		if n.UID == "" || n.ProjectUID == "" {
			return fmt.Errorf("note %q without uid or project_uid", n.UID)
		}
	}
	return nil
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="postit"`)
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(syncapi.Error{Error: msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" && rec.status < 400 {
			return // keep the log free of health checks
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond), "remote", clientIP(r))
	})
}

// clientIP prefers the address the reverse proxy reports.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		ip, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(ip)
	}
	return r.RemoteAddr
}
