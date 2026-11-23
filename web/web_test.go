package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"postit/server"
	"postit/store"
	"postit/syncclient"
	"postit/web"
)

const (
	token    = "0123456789abcdef0123456789abcdef"
	password = "correct horse battery"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	postitd *httptest.Server
	web     *httptest.Server
	st      *store.Store // the web app's replica
	syncer  *web.Syncer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := server.OpenDB(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	postitd := httptest.NewServer(server.Handler(db, token, quiet))
	t.Cleanup(postitd.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	syncer := web.NewSyncer(st, syncclient.New(syncclient.Config{Server: postitd.URL, Token: token}), quiet)
	ws := httptest.NewServer(web.New(st, syncer, password, token, quiet).Handler())
	t.Cleanup(ws.Close)
	return &env{postitd, ws, st, syncer}
}

// browser is an HTTP client with cookies that sends the Origin header a
// browser would send on form posts.
type browser struct {
	*http.Client
	base string
}

type originTransport struct{ origin string }

func (o originTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && o.origin != "" {
		r.Header.Set("Origin", o.origin)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func newBrowser(t *testing.T, base, origin string) browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return browser{&http.Client{Jar: jar, Transport: originTransport{origin}}, base}
}

func (b browser) post(t *testing.T, path string, form url.Values) *http.Response {
	t.Helper()
	resp, err := b.PostForm(b.base+path, form)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (b browser) get(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	resp, err := b.Get(b.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func (e *env) login(t *testing.T) browser {
	t.Helper()
	b := newBrowser(t, e.web.URL, e.web.URL)
	if resp := b.post(t, "/login", url.Values{"password": {password}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	return b
}

func (e *env) sync(t *testing.T) {
	t.Helper()
	if err := e.syncer.SyncNow(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func inboxID(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.InboxID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLogin(t *testing.T) {
	e := newEnv(t)
	b := newBrowser(t, e.web.URL, e.web.URL)

	resp, _ := b.get(t, "/")
	if resp.Request.URL.Path != "/login" {
		t.Fatalf("without session: landed on %s", resp.Request.URL.Path)
	}
	if resp := b.post(t, "/p", url.Values{"name": {"x"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post without session: %d", resp.StatusCode)
	}
	if resp := b.post(t, "/login", url.Values{"password": {"wrong"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if resp := b.post(t, "/login", url.Values{"password": {password}}); resp.StatusCode != http.StatusOK ||
		!strings.HasPrefix(resp.Request.URL.Path, "/p/") {
		t.Fatalf("login: %d at %s", resp.StatusCode, resp.Request.URL.Path)
	}
	resp, body := b.get(t, "/")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Inbox") {
		t.Fatalf("after login: %d", resp.StatusCode)
	}

	// A forged cookie is rejected.
	forged := newBrowser(t, e.web.URL, e.web.URL)
	u, _ := url.Parse(e.web.URL)
	forged.Jar.SetCookies(u, []*http.Cookie{{Name: "postit_session", Value: "99999999999.deadbeef"}})
	if resp, _ := forged.get(t, "/"); resp.Request.URL.Path != "/login" {
		t.Fatal("forged cookie accepted")
	}

	// Logout ends the session in this browser.
	b.post(t, "/logout", nil)
	if resp, _ := b.get(t, "/"); resp.Request.URL.Path != "/login" {
		t.Fatal("still logged in after logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	b := newBrowser(t, e.web.URL, e.web.URL)
	for range 5 {
		b.post(t, "/login", url.Values{"password": {"wrong"}})
	}
	if resp := b.post(t, "/login", url.Values{"password": {password}}); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: %d", resp.StatusCode)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	e := newEnv(t)
	good := e.login(t)
	evil := browser{&http.Client{Jar: good.Jar, Transport: originTransport{"https://evil.example"}}, e.web.URL}
	if resp := evil.post(t, "/p", url.Values{"name": {"Hack"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
	none := browser{&http.Client{Jar: good.Jar}, e.web.URL}
	if resp := none.post(t, "/p", url.Values{"name": {"Hack"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no origin: %d", resp.StatusCode)
	}
	ps, _ := e.st.ListProjects()
	if len(ps) != 1 {
		t.Fatalf("project created despite CSRF check: %v", ps)
	}
}

func TestWebAndDeviceConverge(t *testing.T) {
	e := newEnv(t)
	b := e.login(t)

	// The web app creates a project and a note …
	resp := b.post(t, "/p", url.Values{"name": {"Einkauf"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create project: %d", resp.StatusCode)
	}
	projPath := resp.Request.URL.Path
	b.post(t, projPath+"/notes", url.Values{"body": {"Milch\r\n2 Liter\n\n"}, "color": {"blue"}})
	b.post(t, projPath+"/notes", url.Values{"body": {"   "}}) // dropped like in p
	e.sync(t)

	// … which a device running p receives.
	dev, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	client := syncclient.New(syncclient.Config{Server: e.postitd.URL, Token: token})
	if _, err := client.Sync(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	var projID int64
	ps, _ := dev.ListProjects()
	for _, p := range ps {
		if p.Name == "Einkauf" {
			projID = p.ID
		}
	}
	notes, _ := dev.ListNotes(projID)
	if len(notes) != 1 || notes[0].Body != "Milch\n2 Liter" || notes[0].Color != "blue" {
		t.Fatalf("device sees %+v", notes)
	}

	// The device edits the note; the web board shows it after a sync.
	if err := dev.UpdateNoteBody(notes[0].ID, "Hafermilch"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Sync(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	e.sync(t)
	_, body := b.get(t, projPath)
	if !strings.Contains(body, "Hafermilch") {
		t.Fatal("web board misses the device's change")
	}

	// The web app edits it fully: color, priority, pin, move to the inbox.
	webNotes, _ := e.st.ListNotes(projIDByName(t, e.st, "Einkauf"))
	notePath := "/n/" + itoa(webNotes[0].ID)
	if resp, body := b.get(t, notePath+"?fragment=1"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Hafermilch") {
		t.Fatalf("editor: %d", resp.StatusCode)
	}
	b.post(t, notePath, url.Values{
		"body": {"Hafermilch\nbio"}, "color": {"green"}, "priority": {"2"}, "pinned": {"1"},
		"project": {itoa(inboxID(t, e.st))},
	})
	e.sync(t)
	if _, err := client.Sync(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	inbox, _ := dev.ListNotes(inboxID(t, dev))
	if len(inbox) != 1 {
		t.Fatalf("inbox on device: %+v", inbox)
	}
	if n := inbox[0]; n.Body != "Hafermilch\nbio" || n.Color != "green" || n.Priority != 2 || !n.Pinned {
		t.Fatalf("device sees %+v", n)
	}

	// Deleting the project in the web app removes it on the device.
	b.post(t, projPath+"/delete", nil)
	e.sync(t)
	if _, err := client.Sync(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	ps, _ = dev.ListProjects()
	if len(ps) != 1 || !ps[0].IsInbox {
		t.Fatalf("projects on device after delete: %+v", ps)
	}

	// Deleting the note leaves a tombstone that reaches the device.
	b.post(t, "/n/"+itoa(webNotes[0].ID)+"/delete", nil)
	e.sync(t)
	if _, err := client.Sync(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	if inbox, _ := dev.ListNotes(inboxID(t, dev)); len(inbox) != 0 {
		t.Fatalf("note survived delete: %+v", inbox)
	}
}

func TestInboxProtected(t *testing.T) {
	e := newEnv(t)
	b := e.login(t)
	inbox := "/p/" + itoa(inboxID(t, e.st))
	if resp := b.post(t, inbox+"/delete", nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("delete inbox: %d", resp.StatusCode)
	}
	if resp := b.post(t, inbox+"/rename", url.Values{"name": {"X"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("rename inbox: %d", resp.StatusCode)
	}
	if resp := b.post(t, "/p", url.Values{"name": {"  "}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty project name: %d", resp.StatusCode)
	}
	if resp, _ := b.get(t, "/n/12345"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing note: %d", resp.StatusCode)
	}
}

func TestHealthzAndStatic(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/healthz", "/static/app.css", "/static/app.js", "/static/icon.svg"} {
		resp, err := http.Get(e.web.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

func projIDByName(t *testing.T, st *store.Store, name string) int64 {
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

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
