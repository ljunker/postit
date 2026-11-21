package server_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"postit/server"
	"postit/store"
	"postit/syncclient"
)

const token = "0123456789abcdef0123456789abcdef"

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	db, err := server.OpenDB(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := httptest.NewServer(server.Handler(db, token, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv
}

type device struct {
	st     *store.Store
	client *syncclient.Client
}

func newDevice(t *testing.T, url string) device {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return device{st, syncclient.New(syncclient.Config{Server: url, Token: token})}
}

func (d device) sync(t *testing.T) syncclient.Result {
	t.Helper()
	res, err := d.client.Sync(context.Background(), d.st)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// bodies lists the visible notes per project name.
func (d device) bodies(t *testing.T) map[string][]string {
	t.Helper()
	ps, err := d.st.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, p := range ps {
		ns, _ := d.st.ListNotes(p.ID)
		out[p.Name] = []string{}
		for _, n := range ns {
			out[p.Name] = append(out[p.Name], n.Body+"/"+n.Color)
		}
	}
	return out
}

func projectID(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	ps, _ := st.ListProjects()
	for _, p := range ps {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no project %q", name)
	return 0
}

func noteID(t *testing.T, st *store.Store, project, body string) int64 {
	t.Helper()
	ns, _ := st.ListNotes(projectID(t, st, project))
	for _, n := range ns {
		if n.Body == body {
			return n.ID
		}
	}
	t.Fatalf("no note %q in %q", body, project)
	return 0
}

func TestAuth(t *testing.T) {
	srv := newServer(t)
	for _, tc := range []struct {
		path, auth string
		want       int
	}{
		{"/healthz", "", http.StatusNoContent},
		{"/api/v1/ping", "", http.StatusUnauthorized},
		{"/api/v1/ping", "Bearer wrong", http.StatusUnauthorized},
		{"/api/v1/ping", "Bearer " + token, http.StatusNoContent},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s %q: %d, want %d", tc.path, tc.auth, resp.StatusCode, tc.want)
		}
	}

	bad := syncclient.New(syncclient.Config{Server: srv.URL, Token: "wrong"})
	if err := bad.Ping(context.Background()); !errors.Is(err, syncclient.ErrUnauthorized) {
		t.Fatalf("wrong token: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/v1/sync", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sync without token: %d", resp.StatusCode)
	}
}

func TestTwoDevicesConverge(t *testing.T) {
	srv := newServer(t)
	a, b := newDevice(t, srv.URL), newDevice(t, srv.URL)

	// A creates data; B receives it; both share one inbox.
	work, _ := a.st.CreateProject("Work")
	_, _ = a.st.CreateNote(work, "plan", "blue")
	inboxA, _ := a.st.InboxID()
	_, _ = a.st.CreateNote(inboxA, "milk", "")
	a.sync(t)
	if res := b.sync(t); !res.Changed {
		t.Fatal("B saw no change")
	}
	a.sync(t)
	if got, want := b.bodies(t), a.bodies(t); !equal(got, want) {
		t.Fatalf("after first sync\nA: %v\nB: %v", want, got)
	}
	if ps, _ := b.st.ListProjects(); len(ps) != 2 {
		t.Fatalf("B has %d projects, want inbox + Work", len(ps))
	}

	// Both edit the same note: the later edit wins everywhere.
	planA, planB := noteID(t, a.st, "Work", "plan"), noteID(t, b.st, "Work", "plan")
	_ = a.st.UpdateNoteBody(planA, "plan A")
	_ = b.st.UpdateNoteBody(planB, "plan B")
	a.sync(t)
	b.sync(t) // B is newer and wins on the server
	a.sync(t)
	if got := a.bodies(t)["Work"]; len(got) != 1 || got[0] != "plan B/blue" {
		t.Fatalf("A after conflict: %v", got)
	}

	// A later change on A reaches B.
	_ = a.st.SetColor(noteID(t, a.st, "Work", "plan B"), "green")
	a.sync(t)
	b.sync(t)
	if got := b.bodies(t)["Work"]; got[0] != "plan B/green" {
		t.Fatalf("B did not get the color: %v", got)
	}

	// Move, delete a project, then sync both ways.
	inboxB, _ := b.st.InboxID()
	_ = b.st.MoveNote(noteID(t, b.st, "Work", "plan B"), inboxB)
	b.sync(t)
	a.sync(t)
	_ = a.st.DeleteProject(projectID(t, a.st, "Work"))
	a.sync(t)
	b.sync(t)
	want := map[string][]string{store.InboxName: {"plan B/green", "milk/yellow"}}
	for name, d := range map[string]device{"A": a, "B": b} {
		got := d.bodies(t)
		if len(got[store.InboxName]) != 2 || len(got) != 1 {
			t.Fatalf("%s at the end: %v, want %v", name, got, want)
		}
	}

	// Nothing left to send, and a repeated sync changes nothing.
	for _, d := range []device{a, b} {
		if pending, _ := d.st.HasPending(); pending {
			t.Fatal("still pending")
		}
		if res := d.sync(t); res.Changed || res.Pushed != 0 {
			t.Fatalf("idle sync: %+v", res)
		}
	}
}

func equal(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || strings.Join(va, "|") != strings.Join(vb, "|") {
			return false
		}
	}
	return true
}
