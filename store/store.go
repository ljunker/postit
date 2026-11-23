// Package store keeps projects and post-it notes in a SQLite database.
//
// Rows are never deleted but marked deleted, and every change sets
// modified_at and the dirty flag, so that the sync package can send local
// changes to the server and merge the server's changes (see sync.go).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"postit/syncapi"
)

// InboxName is the name of the system project quick-added notes land in.
const InboxName = "Inbox"

// MaxPriority is the highest priority a note can have (0 = none).
const MaxPriority = 3

type Project struct {
	ID        int64
	Name      string
	IsInbox   bool
	NoteCount int
	ParentID  int64 // 0 = top level (also when the stored parent is gone)
	Depth     int   // 0 = top level
}

type Note struct {
	ID        int64
	ProjectID int64
	Body      string
	Color     string
	Priority  int
	Pinned    bool
	UpdatedAt time.Time
}

// Title is the first line of the note's body.
func (n Note) Title() string {
	return Title(n.Body)
}

// Title returns the first line of body, trimmed.
func Title(body string) string {
	line, _, _ := strings.Cut(strings.TrimLeft(body, "\n"), "\n")
	return strings.TrimSpace(line)
}

type Store struct {
	db      *sql.DB
	changes atomic.Uint64
}

const baseSchema = `
CREATE TABLE IF NOT EXISTS projects (
	id         INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	position   INTEGER NOT NULL DEFAULT 0,
	is_inbox   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS notes (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	body       TEXT    NOT NULL DEFAULT '',
	color      TEXT    NOT NULL DEFAULT 'yellow',
	priority   INTEGER NOT NULL DEFAULT 0,
	pinned     INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_project ON notes(project_id);
`

// migrateV1 adds what syncing needs: a global uid, a change clock, tombstones
// and a dirty flag per row, and a table for the sync cursor.
const migrateV1 = `
ALTER TABLE projects ADD COLUMN uid         TEXT;
ALTER TABLE projects ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN deleted     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE projects ADD COLUMN dirty       INTEGER NOT NULL DEFAULT 1;
ALTER TABLE notes    ADD COLUMN uid         TEXT;
ALTER TABLE notes    ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notes    ADD COLUMN deleted     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE notes    ADD COLUMN dirty       INTEGER NOT NULL DEFAULT 1;
CREATE TABLE IF NOT EXISTS sync_state (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
UPDATE projects SET modified_at = created_at;
UPDATE notes    SET modified_at = updated_at;
UPDATE projects SET uid = '` + syncapi.InboxUID + `' WHERE id = (SELECT MIN(id) FROM projects WHERE is_inbox = 1);
`

const migrateV1Indexes = `
CREATE UNIQUE INDEX projects_uid ON projects(uid);
CREATE UNIQUE INDEX notes_uid    ON notes(uid);
CREATE INDEX projects_dirty ON projects(dirty) WHERE dirty = 1;
CREATE INDEX notes_dirty    ON notes(dirty)    WHERE dirty = 1;
`

// migrateV2 lets projects nest: parent_uid is the parent's uid, ” = top level.
// position then orders siblings.
const migrateV2 = `
ALTER TABLE projects ADD COLUMN parent_uid TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 2;
`

// touch marks a row as changed. The clock never goes backwards for a row, so a
// later local change always wins over an earlier one even if the clock jumps.
const touch = `modified_at = MAX(?, modified_at + 1), dirty = 1`

// DefaultPath is ~/.local/share/postit/postit.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "postit", "postit.db"), nil
}

// Open opens (and if needed creates or migrates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if _, err := s.InboxID(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(baseSchema); err != nil {
		return err
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 1 {
		if err := s.migrateToV1(); err != nil {
			return err
		}
	}
	if version < 2 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(migrateV2); err != nil {
			return err
		}
		return tx.Commit()
	}
	return nil
}

func (s *Store) migrateToV1() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(migrateV1); err != nil {
		return err
	}
	for _, table := range []string{"projects", "notes"} {
		if err := fillUIDs(tx, table); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(migrateV1Indexes); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

func fillUIDs(tx *sql.Tx, table string) error {
	rows, err := tx.Query(`SELECT id FROM ` + table + ` WHERE uid IS NULL`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`UPDATE `+table+` SET uid = ? WHERE id = ?`, newUID(), id); err != nil {
			return err
		}
	}
	return nil
}

func newUID() string { return uuid.NewString() }

func (s *Store) Close() error { return s.db.Close() }

// Changes counts local changes since Open. It does not count changes merged
// from the server; the UI uses it to know when to sync.
func (s *Store) Changes() uint64 { return s.changes.Load() }

func now() int64 { return time.Now().UnixNano() }

// exec runs a local change and counts it.
func (s *Store) exec(query string, args ...any) (sql.Result, error) {
	res, err := s.db.Exec(query, args...)
	if err == nil {
		s.changes.Add(1)
	}
	return res, err
}

// InboxID returns the inbox project's id, creating the inbox if missing.
func (s *Store) InboxID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM projects WHERE uid = ?`, syncapi.InboxUID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	t := now()
	res, err := s.exec(`INSERT INTO projects(uid, name, position, is_inbox, created_at, modified_at) VALUES (?, ?, -1, 1, ?, ?)`,
		syncapi.InboxUID, InboxName, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// querier is *sql.DB or *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// ListProjects returns the inbox first, then the project tree depth-first:
// every project is followed by its subprojects (Depth one higher). Siblings
// keep their order (see MoveProject; new projects go to the end). Ties, e.g.
// after two devices reordered at once, break by creation time so every device
// agrees. A project whose parent is gone, or that ended up in a cycle because
// two devices nested projects into each other at once, is shown at the top
// level; nothing is written to repair it.
func (s *Store) ListProjects() ([]Project, error) {
	return listProjects(s.db)
}

func listProjects(q querier) ([]Project, error) {
	rows, err := q.Query(`
		SELECT p.id, p.name, p.is_inbox, COUNT(n.id), p.uid, p.parent_uid
		FROM projects p LEFT JOIN notes n ON n.project_id = p.id AND n.deleted = 0
		WHERE p.deleted = 0
		GROUP BY p.id
		ORDER BY p.is_inbox DESC, p.position, p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type node struct {
		p         Project
		uid, puid string
	}
	var all []node
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.p.ID, &n.p.Name, &n.p.IsInbox, &n.p.NoteCount, &n.uid, &n.puid); err != nil {
			return nil, err
		}
		all = append(all, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	byUID := make(map[string]int, len(all))
	for i, n := range all {
		byUID[n.uid] = i
	}
	children := make(map[int][]int) // parent index -> child indexes, in order
	var roots []int
	for i, n := range all {
		pi, ok := byUID[n.puid]
		if ok && n.puid != "" && !n.p.IsInbox && !all[pi].p.IsInbox {
			children[pi] = append(children[pi], i)
		} else {
			roots = append(roots, i)
		}
	}

	out := make([]Project, 0, len(all))
	seen := make([]bool, len(all))
	var walk func(i, depth int, parent int64)
	walk = func(i, depth int, parent int64) {
		seen[i] = true
		p := all[i].p
		p.Depth, p.ParentID = depth, parent
		out = append(out, p)
		for _, c := range children[i] {
			if !seen[c] {
				walk(c, depth+1, p.ID)
			}
		}
	}
	for _, i := range roots {
		walk(i, 0, 0)
	}
	// Whatever is left sits in a cycle: show it at the top level, in order.
	for i := range all {
		if !seen[i] {
			walk(i, 0, 0)
		}
	}
	return out, nil
}

// SiblingIndex returns the index of project id among the projects that share
// its parent (the inbox excluded; -1 if id is the inbox or missing) and how
// many such siblings there are. projects is a list from ListProjects.
func SiblingIndex(projects []Project, id int64) (index, n int) {
	p, ok := find(projects, id)
	if !ok || p.IsInbox {
		return -1, 0
	}
	index = -1
	for _, q := range siblings(projects, p.ParentID) {
		if q == id {
			index = n
		}
		n++
	}
	return index, n
}

// Descendants returns the ids of all projects below id in projects (a list
// from ListProjects), not including id.
func Descendants(projects []Project, id int64) map[int64]bool {
	out := map[int64]bool{}
	for i, p := range projects {
		if p.ID != id {
			continue
		}
		for _, q := range projects[i+1:] {
			if q.Depth <= p.Depth {
				break
			}
			out[q.ID] = true
		}
		break
	}
	return out
}

func find(projects []Project, id int64) (Project, bool) {
	for _, p := range projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// siblings returns the ids of the projects under parent (0 = top level, the
// inbox excluded) in their order.
func siblings(projects []Project, parent int64) []int64 {
	var out []int64
	for _, p := range projects {
		if !p.IsInbox && p.ParentID == parent {
			out = append(out, p.ID)
		}
	}
	return out
}

func (s *Store) CreateProject(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("project name is empty")
	}
	t := now()
	res, err := s.exec(`INSERT INTO projects(uid, name, position, created_at, modified_at)
		VALUES (?, ?, COALESCE((SELECT MAX(position) FROM projects WHERE parent_uid = ''), 0) + 1, ?, ?)`, newUID(), name, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) RenameProject(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("project name is empty")
	}
	_, err := s.exec(`UPDATE projects SET name = ?, `+touch+` WHERE id = ? AND is_inbox = 0`, name, now(), id)
	return err
}

// ErrNotMovable is returned by MoveProject and SetParent for the inbox or an
// unknown project.
var ErrNotMovable = errors.New("project cannot be moved")

// ErrCycle is returned by SetParent when the new parent is the project
// itself, one of its subprojects, the inbox or unknown.
var ErrCycle = errors.New("project cannot go there")

// MoveProject puts a project at index among its siblings (0 = first); index
// is clamped to the list. The inbox stays first. Sibling positions are
// renumbered 1..n and only rows whose position changes are marked for sync.
func (s *Store) MoveProject(id int64, index int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	projects, err := listProjects(tx)
	if err != nil {
		return err
	}
	p, ok := find(projects, id)
	if !ok || p.IsInbox {
		return ErrNotMovable
	}
	var list []int64
	for _, q := range siblings(projects, p.ParentID) {
		if q != id {
			list = append(list, q)
		}
	}
	index = max(0, min(index, len(list)))
	list = append(list[:index], append([]int64{id}, list[index:]...)...)

	changed, err := renumber(tx, list)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed {
		s.changes.Add(1)
	}
	return nil
}

// renumber sets positions 1..n for ids in order and marks only rows whose
// position changes. It reports whether any row changed.
func renumber(tx *sql.Tx, ids []int64) (bool, error) {
	t := now()
	changed := false
	for i, id := range ids {
		res, err := tx.Exec(`UPDATE projects SET position = ?, `+touch+` WHERE id = ? AND position != ?`, i+1, t, id, i+1)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
	}
	return changed, nil
}

// SetParent makes a project a subproject of parentID (0 = top level) and puts
// it last among its new siblings.
func (s *Store) SetParent(id, parentID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	projects, err := listProjects(tx)
	if err != nil {
		return err
	}
	p, ok := find(projects, id)
	if !ok || p.IsInbox {
		return ErrNotMovable
	}
	if p.ParentID == parentID {
		return nil
	}
	parentUID := ""
	if parentID != 0 {
		parent, ok := find(projects, parentID)
		if !ok || parent.IsInbox || parentID == id || Descendants(projects, id)[parentID] {
			return ErrCycle
		}
		if err := tx.QueryRow(`SELECT uid FROM projects WHERE id = ?`, parentID).Scan(&parentUID); err != nil {
			return err
		}
	}
	last := len(siblings(projects, parentID)) + 1
	if _, err := tx.Exec(`UPDATE projects SET parent_uid = ?, position = ?, `+touch+` WHERE id = ?`,
		parentUID, last, now(), id); err != nil {
		return err
	}
	// Close the gap left behind.
	var rest []int64
	for _, q := range siblings(projects, p.ParentID) {
		if q != id {
			rest = append(rest, q)
		}
	}
	if _, err := renumber(tx, rest); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changes.Add(1)
	return nil
}

// DeleteProject deletes a project and its own notes. Its subprojects move one
// level up, after the deleted project's siblings, keeping their order. The
// inbox cannot be deleted.
func (s *Store) DeleteProject(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	projects, err := listProjects(tx)
	if err != nil {
		return err
	}
	p, ok := find(projects, id)
	if !ok || p.IsInbox {
		return nil
	}
	newParent := ""
	if p.ParentID != 0 {
		if err := tx.QueryRow(`SELECT uid FROM projects WHERE id = ?`, p.ParentID).Scan(&newParent); err != nil {
			return err
		}
	}
	t := now()
	var order []int64 // the remaining siblings, then the children
	for _, q := range siblings(projects, p.ParentID) {
		if q != id {
			order = append(order, q)
		}
	}
	for _, c := range siblings(projects, id) {
		if _, err := tx.Exec(`UPDATE projects SET parent_uid = ?, `+touch+` WHERE id = ?`, newParent, t, c); err != nil {
			return err
		}
		order = append(order, c)
	}
	if _, err := renumber(tx, order); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE projects SET deleted = 1, `+touch+` WHERE id = ?`, t, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE notes SET deleted = 1, `+touch+` WHERE project_id = ? AND deleted = 0`, t, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.changes.Add(1)
	return nil
}

const noteCols = `id, project_id, body, color, priority, pinned, updated_at`

func scanNote(sc interface{ Scan(...any) error }) (Note, error) {
	var n Note
	var upd int64
	err := sc.Scan(&n.ID, &n.ProjectID, &n.Body, &n.Color, &n.Priority, &n.Pinned, &upd)
	n.UpdatedAt = time.Unix(0, upd)
	return n, err
}

// ListNotes returns a project's notes: pinned first, then by priority, then newest.
func (s *Store) ListNotes(projectID int64) ([]Note, error) {
	rows, err := s.db.Query(`
		SELECT `+noteCols+`
		FROM notes WHERE project_id = ? AND deleted = 0
		ORDER BY pinned DESC, priority DESC, updated_at DESC, id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNote(id int64) (Note, error) {
	return scanNote(s.db.QueryRow(`SELECT `+noteCols+` FROM notes WHERE id = ? AND deleted = 0`, id))
}

func (s *Store) CreateNote(projectID int64, body, color string) (int64, error) {
	if color == "" {
		color = "yellow"
	}
	t := now()
	res, err := s.exec(`INSERT INTO notes(uid, project_id, body, color, created_at, updated_at, modified_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newUID(), projectID, body, color, t, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SaveNote writes body, color, priority and pinned of n.
func (s *Store) SaveNote(n Note) error {
	t := now()
	_, err := s.exec(`UPDATE notes SET body = ?, color = ?, priority = ?, pinned = ?, updated_at = ?, `+touch+` WHERE id = ?`,
		n.Body, n.Color, clampPrio(n.Priority), n.Pinned, t, t, n.ID)
	return err
}

func (s *Store) UpdateNoteBody(id int64, body string) error {
	t := now()
	_, err := s.exec(`UPDATE notes SET body = ?, updated_at = ?, `+touch+` WHERE id = ?`, body, t, t, id)
	return err
}

func (s *Store) SetColor(id int64, color string) error {
	_, err := s.exec(`UPDATE notes SET color = ?, `+touch+` WHERE id = ?`, color, now(), id)
	return err
}

func (s *Store) SetPriority(id int64, prio int) error {
	_, err := s.exec(`UPDATE notes SET priority = ?, `+touch+` WHERE id = ?`, clampPrio(prio), now(), id)
	return err
}

func (s *Store) TogglePinned(id int64) error {
	_, err := s.exec(`UPDATE notes SET pinned = 1 - pinned, `+touch+` WHERE id = ?`, now(), id)
	return err
}

func (s *Store) MoveNote(id, projectID int64) error {
	_, err := s.exec(`UPDATE notes SET project_id = ?, `+touch+` WHERE id = ?`, projectID, now(), id)
	return err
}

func (s *Store) DeleteNote(id int64) error {
	_, err := s.exec(`UPDATE notes SET deleted = 1, `+touch+` WHERE id = ?`, now(), id)
	return err
}

func clampPrio(p int) int {
	return max(0, min(MaxPriority, p))
}
