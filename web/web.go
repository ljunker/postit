// Package web is the browser UI for postit. It keeps its own replica of the
// notes in a store.Store and syncs it with postitd like any other device, so
// the sync token never reaches the browser.
package web

import (
	"database/sql"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"postit/md"
	"postit/palette"
	"postit/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Before a page is shown, sync if the last attempt is older than freshAge,
// but never keep the browser waiting longer than freshWait.
const (
	freshAge  = 5 * time.Second
	freshWait = 3 * time.Second
)

type App struct {
	st   *store.Store
	sync *Syncer
	auth *auth
	log  *slog.Logger
	tmpl *template.Template
}

// New returns the web app. password is the login password, token the sync
// token (it only feeds the session key here).
func New(st *store.Store, syncer *Syncer, password, token string, log *slog.Logger) *App {
	return &App{
		st:   st,
		sync: syncer,
		auth: newAuth(password, token),
		log:  log,
		tmpl: template.Must(template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")),
	}
}

var funcs = template.FuncMap{
	"seq": func(n int) []int {
		out := make([]int, n+1)
		for i := range out {
			out[i] = i
		}
		return out
	},
	"repeat": strings.Repeat,
}

// Handler returns the app's routes.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", cacheStatic(http.StripPrefix("/static/", http.FileServerFS(static))))
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.login)

	app := http.NewServeMux()
	app.HandleFunc("POST /logout", a.logout)
	app.HandleFunc("GET /{$}", a.home)
	app.HandleFunc("GET /p/{id}", a.projectPage)
	app.HandleFunc("POST /p", a.createProject)
	app.HandleFunc("POST /p/{id}/rename", a.renameProject)
	app.HandleFunc("POST /p/{id}/delete", a.deleteProject)
	app.HandleFunc("POST /p/{id}/move", a.moveProject)
	app.HandleFunc("POST /p/{id}/notes", a.createNote)
	app.HandleFunc("GET /n/{id}", a.notePage)
	app.HandleFunc("POST /n/{id}", a.saveNote)
	app.HandleFunc("POST /n/{id}/pin", a.pinNote)
	app.HandleFunc("POST /n/{id}/delete", a.deleteNote)
	app.HandleFunc("POST /preview", a.preview)
	mux.Handle("/", a.requireLogin(app))

	return securityHeaders(logRequests(a.log, mux))
}

func (a *App) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.auth.loggedIn(r) {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
			}
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			http.Error(w, "Anfrage von fremder Seite abgelehnt", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- pages -----------------------------------------------------------------

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	if _, err := a.st.InboxID(); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type loginData struct{ Error string }

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.auth.loggedIn(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, http.StatusOK, "login", loginData{})
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	ip, now := clientIP(r), time.Now()
	if a.auth.tooManyFailures(ip, now) {
		a.render(w, http.StatusTooManyRequests, "login", loginData{"Zu viele Fehlversuche – bitte eine Minute warten."})
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "Anfrage von fremder Seite abgelehnt", http.StatusForbidden)
		return
	}
	if !a.auth.checkPassword(r.PostFormValue("password")) {
		a.auth.recordFailure(ip, now)
		a.log.Warn("login failed", "remote", ip)
		a.render(w, http.StatusUnauthorized, "login", loginData{"Passwort falsch."})
		return
	}
	a.auth.setCookie(w, r)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	id, err := a.st.InboxID()
	if a.fail(w, err) {
		return
	}
	http.Redirect(w, r, projectURL(id), http.StatusSeeOther)
}

type noteView struct {
	store.Note
	Color   palette.Color
	Title   string        // first line as plain text
	Rest    template.HTML // the rest, Markdown rendered
	Updated string
}

type syncView struct {
	OK      bool
	Offline bool
	Text    string
}

type pageData struct {
	Projects []store.Project
	Current  store.Project
	Notes    []noteView
	Sync     syncView
	CanUp    bool // the current project can move up / down
	CanDown  bool
}

func (a *App) projectPage(w http.ResponseWriter, r *http.Request) {
	a.sync.Fresh(r.Context(), freshAge, freshWait)
	projects, cur, ok := a.project(w, r)
	if !ok {
		return
	}
	notes, err := a.st.ListNotes(cur.ID)
	if a.fail(w, err) {
		return
	}
	data := pageData{Projects: projects, Current: cur, Sync: a.syncView()}
	if i, n := movableIndex(projects, cur.ID); i >= 0 {
		data.CanUp, data.CanDown = i > 0, i < n-1
	}
	for _, n := range notes {
		data.Notes = append(data.Notes, newNoteView(n))
	}
	name := "page"
	if r.URL.Query().Has("fragment") {
		name = "app"
	}
	a.render(w, http.StatusOK, name, data)
}

func newNoteView(n store.Note) noteView {
	return noteView{
		Note:    n,
		Color:   palette.ByName(n.Color),
		Title:   md.Title(n.Body),
		Rest:    md.HTML(md.Rest(n.Body)),
		Updated: n.UpdatedAt.Local().Format("02.01.06 15:04"),
	}
}

func (a *App) syncView() syncView {
	ok, err := a.sync.Status()
	switch {
	case err != nil:
		v := syncView{Offline: true, Text: "offline"}
		if !ok.IsZero() {
			v.Text += " · zuletzt synchronisiert " + ok.Local().Format("15:04:05")
		}
		return v
	case !ok.IsZero():
		return syncView{OK: true, Text: "synchronisiert " + ok.Local().Format("15:04:05")}
	}
	return syncView{Text: "noch nicht synchronisiert"}
}

type editorData struct {
	Note     store.Note
	Color    palette.Color
	Projects []store.Project
	Palette  []palette.Color
	Back     string
}

func (a *App) notePage(w http.ResponseWriter, r *http.Request) {
	n, ok := a.note(w, r)
	if !ok {
		return
	}
	projects, err := a.st.ListProjects()
	if a.fail(w, err) {
		return
	}
	n.Color = palette.ByName(n.Color).Name
	data := editorData{Note: n, Color: palette.ByName(n.Color), Projects: projects, Palette: palette.Colors, Back: projectURL(n.ProjectID)}
	name := "editorPage"
	if r.URL.Query().Has("fragment") {
		name = "editor"
	}
	a.render(w, http.StatusOK, name, data)
}

// ---- projects --------------------------------------------------------------

func (a *App) createProject(w http.ResponseWriter, r *http.Request) {
	id, err := a.st.CreateProject(r.PostFormValue("name"))
	if a.userError(w, err) {
		return
	}
	a.changed(w, r, projectURL(id))
}

func (a *App) renameProject(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.project(w, r)
	if !ok {
		return
	}
	if p.IsInbox {
		http.Error(w, "Die Inbox kann nicht umbenannt werden.", http.StatusBadRequest)
		return
	}
	if a.userError(w, a.st.RenameProject(p.ID, r.PostFormValue("name"))) {
		return
	}
	a.changed(w, r, projectURL(p.ID))
}

func (a *App) deleteProject(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.project(w, r)
	if !ok {
		return
	}
	if p.IsInbox {
		http.Error(w, "Die Inbox kann nicht gelöscht werden.", http.StatusBadRequest)
		return
	}
	if a.fail(w, a.st.DeleteProject(p.ID)) {
		return
	}
	a.changed(w, r, "/")
}

// moveProject reorders a project: dir=up|down moves it one step, to=N puts
// it at index N among the projects below the inbox (drag and drop).
func (a *App) moveProject(w http.ResponseWriter, r *http.Request) {
	projects, p, ok := a.project(w, r)
	if !ok {
		return
	}
	if p.IsInbox {
		http.Error(w, "Die Inbox bleibt oben.", http.StatusBadRequest)
		return
	}
	cur, _ := movableIndex(projects, p.ID)
	var to int
	switch dir := r.PostFormValue("dir"); {
	case dir == "up":
		to = cur - 1
	case dir == "down":
		to = cur + 1
	default:
		n, err := strconv.Atoi(r.PostFormValue("to"))
		if err != nil {
			http.Error(w, "dir=up|down oder to=<Index> fehlt", http.StatusBadRequest)
			return
		}
		to = n
	}
	if a.fail(w, a.st.MoveProject(p.ID, to)) {
		return
	}
	a.changed(w, r, projectURL(p.ID))
}

// movableIndex returns the index of project id among the projects below the
// inbox (-1 if it is the inbox or missing) and how many of those there are.
func movableIndex(projects []store.Project, id int64) (index, n int) {
	index = -1
	for _, p := range projects {
		if p.IsInbox {
			continue
		}
		if p.ID == id {
			index = n
		}
		n++
	}
	return index, n
}

// ---- notes -----------------------------------------------------------------

func (a *App) createNote(w http.ResponseWriter, r *http.Request) {
	_, p, ok := a.project(w, r)
	if !ok {
		return
	}
	body := cleanBody(r.PostFormValue("body"))
	if strings.TrimSpace(body) == "" { // like p: a new note with no text is dropped
		http.Redirect(w, r, projectURL(p.ID), http.StatusSeeOther)
		return
	}
	color := r.PostFormValue("color")
	if !palette.Valid(color) {
		color = palette.Colors[0].Name
	}
	_, err := a.st.CreateNote(p.ID, body, color)
	if a.fail(w, err) {
		return
	}
	a.changed(w, r, projectURL(p.ID))
}

// saveNote stores the editor form: body, color, priority, pin and project.
func (a *App) saveNote(w http.ResponseWriter, r *http.Request) {
	n, ok := a.note(w, r)
	if !ok {
		return
	}
	back := projectURL(n.ProjectID)
	n.Body = cleanBody(r.PostFormValue("body"))
	if c := r.PostFormValue("color"); palette.Valid(c) {
		n.Color = c
	}
	if p, err := strconv.Atoi(r.PostFormValue("priority")); err == nil {
		n.Priority = p // the store clamps it
	}
	n.Pinned = r.PostFormValue("pinned") != ""
	if a.fail(w, a.st.SaveNote(n)) {
		return
	}
	if target, err := strconv.ParseInt(r.PostFormValue("project"), 10, 64); err == nil && target != n.ProjectID {
		if !a.projectExists(target) {
			http.Error(w, "Projekt nicht gefunden", http.StatusBadRequest)
			return
		}
		if a.fail(w, a.st.MoveNote(n.ID, target)) {
			return
		}
	}
	a.changed(w, r, back)
}

func (a *App) pinNote(w http.ResponseWriter, r *http.Request) {
	n, ok := a.note(w, r)
	if !ok {
		return
	}
	if a.fail(w, a.st.TogglePinned(n.ID)) {
		return
	}
	a.changed(w, r, projectURL(n.ProjectID))
}

func (a *App) deleteNote(w http.ResponseWriter, r *http.Request) {
	n, ok := a.note(w, r)
	if !ok {
		return
	}
	if a.fail(w, a.st.DeleteNote(n.ID)) {
		return
	}
	a.changed(w, r, projectURL(n.ProjectID))
}

// preview renders the editor's text as it will look on the card.
func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	body := cleanBody(r.PostFormValue("body"))
	a.render(w, http.StatusOK, "preview", noteView{Title: md.Title(body), Rest: md.HTML(md.Rest(body))})
}

// cleanBody normalises line endings and drops trailing blanks, like p.
func cleanBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, " \n")
}

// ---- helpers ---------------------------------------------------------------

// changed schedules a sync and sends the browser back to a page.
func (a *App) changed(w http.ResponseWriter, r *http.Request, to string) {
	a.sync.Changed()
	if next := r.PostFormValue("next"); strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		to = next
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func projectURL(id int64) string { return "/p/" + strconv.FormatInt(id, 10) }

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

// project resolves {id} to a live project, answering 404 otherwise.
func (a *App) project(w http.ResponseWriter, r *http.Request) ([]store.Project, store.Project, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return nil, store.Project{}, false
	}
	projects, err := a.st.ListProjects()
	if a.fail(w, err) {
		return nil, store.Project{}, false
	}
	for _, p := range projects {
		if p.ID == id {
			return projects, p, true
		}
	}
	if r.Method == http.MethodGet { // deleted elsewhere: back to the inbox
		http.Redirect(w, r, "/", http.StatusSeeOther)
	} else {
		http.NotFound(w, r)
	}
	return nil, store.Project{}, false
}

func (a *App) projectExists(id int64) bool {
	projects, err := a.st.ListProjects()
	if err != nil {
		return false
	}
	for _, p := range projects {
		if p.ID == id {
			return true
		}
	}
	return false
}

// note resolves {id} to a live note, answering 404 otherwise.
func (a *App) note(w http.ResponseWriter, r *http.Request) (store.Note, bool) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return store.Note{}, false
	}
	n, err := a.st.GetNote(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Post-it nicht gefunden – vielleicht auf einem anderen Gerät gelöscht.", http.StatusNotFound)
		return n, false
	}
	return n, !a.fail(w, err)
}

func (a *App) fail(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	a.log.Error("request", "err", err)
	http.Error(w, "interner Fehler", http.StatusInternalServerError)
	return true
}

// userError answers 400 with the store's message (e.g. empty name).
func (a *App) userError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
	return true
}

func (a *App) render(w http.ResponseWriter, code int, name string, data any) {
	var buf strings.Builder
	if err := a.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		a.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	w.Write([]byte(buf.String()))
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
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
		if rec.status < 400 && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			return // only changes and errors
		}
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond), "remote", clientIP(r))
	})
}
