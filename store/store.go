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
	if version >= 1 {
		return nil
	}
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

// ListProjects returns the inbox first, then projects in their order (see
// MoveProject; new projects go to the end). Ties, e.g. after two devices
// reordered at once, break by creation time so every device agrees.
func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, p.is_inbox, COUNT(n.id)
		FROM projects p LEFT JOIN notes n ON n.project_id = p.id AND n.deleted = 0
		WHERE p.deleted = 0
		GROUP BY p.id
		ORDER BY p.is_inbox DESC, p.position, p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.IsInbox, &p.NoteCount); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateProject(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("project name is empty")
	}
	t := now()
	res, err := s.exec(`INSERT INTO projects(uid, name, position, created_at, modified_at)
		VALUES (?, ?, COALESCE((SELECT MAX(position) FROM projects), 0) + 1, ?, ?)`, newUID(), name, t, t)
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

// ErrNotMovable is returned by MoveProject for the inbox or an unknown project.
var ErrNotMovable = errors.New("project cannot be moved")

// MoveProject puts a project at index among the projects below the inbox
// (0 = right below it); index is clamped to the list. The inbox stays first.
// Positions are renumbered 1..n and only rows whose position changes are
// marked for sync.
func (s *Store) MoveProject(id int64, index int) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type row struct{ id, pos int64 }
	rows, err := tx.Query(`SELECT id, position FROM projects WHERE deleted = 0 AND is_inbox = 0
		ORDER BY position, created_at, id`)
	if err != nil {
		return err
	}
	var list []row
	from := -1
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.pos); err != nil {
			rows.Close()
			return err
		}
		if r.id == id {
			from = len(list)
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if from < 0 {
		return ErrNotMovable
	}
	index = max(0, min(index, len(list)-1))
	r := list[from]
	list = append(list[:from], list[from+1:]...)
	list = append(list[:index], append([]row{r}, list[index:]...)...)

	t := now()
	changed := false
	for i, r := range list {
		if r.pos == int64(i+1) {
			continue
		}
		if _, err := tx.Exec(`UPDATE projects SET position = ?, `+touch+` WHERE id = ?`, i+1, t, r.id); err != nil {
			return err
		}
		changed = true
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if changed {
		s.changes.Add(1)
	}
	return nil
}

// DeleteProject deletes a project and all its notes. The inbox cannot be deleted.
func (s *Store) DeleteProject(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t := now()
	res, err := tx.Exec(`UPDATE projects SET deleted = 1, `+touch+` WHERE id = ? AND is_inbox = 0 AND deleted = 0`, t, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
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
