// Package server is the postitd sync service: it keeps the master copy of all
// projects and post-its and merges the changes devices send (see syncapi).
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"postit/syncapi"
)

// DB is the server's SQLite database. Every row carries rev, the value of a
// global counter at the time the row last changed; clients ask for rev > cursor.
type DB struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	uid         TEXT PRIMARY KEY,
	name        TEXT    NOT NULL,
	parent_uid  TEXT    NOT NULL DEFAULT '',
	position    INTEGER NOT NULL,
	is_inbox    INTEGER NOT NULL,
	deleted     INTEGER NOT NULL,
	created_at  INTEGER NOT NULL,
	modified_at INTEGER NOT NULL,
	rev         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS projects_rev ON projects(rev);
CREATE TABLE IF NOT EXISTS notes (
	uid         TEXT PRIMARY KEY,
	project_uid TEXT    NOT NULL,
	body        TEXT    NOT NULL,
	color       TEXT    NOT NULL,
	priority    INTEGER NOT NULL,
	pinned      INTEGER NOT NULL,
	deleted     INTEGER NOT NULL,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	modified_at INTEGER NOT NULL,
	rev         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_rev ON notes(rev);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
INSERT OR IGNORE INTO meta(key, value) VALUES ('rev', 0);
`

// OpenDB opens (and if needed creates) the database at path.
func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return &DB{db: db}, nil
}

// migrate adds columns that databases created by older versions lack.
func migrate(db *sql.DB) error {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'parent_uid'`).Scan(&n)
	if err != nil || n > 0 {
		return err
	}
	_, err = db.Exec(`ALTER TABLE projects ADD COLUMN parent_uid TEXT NOT NULL DEFAULT ''`)
	return err
}

func (d *DB) Close() error { return d.db.Close() }

// Ping checks that the database answers.
func (d *DB) Ping() error { return d.db.Ping() }

// Sync merges req (last write wins by ModifiedAt) and returns everything that
// changed after req.Since plus the server's version of each rejected row.
func (d *DB) Sync(req syncapi.Request) (syncapi.Response, error) {
	var resp syncapi.Response
	tx, err := d.db.Begin()
	if err != nil {
		return resp, err
	}
	defer tx.Rollback()

	var rev int64
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'rev'`).Scan(&rev); err != nil {
		return resp, err
	}

	var rejectedProjects, rejectedNotes []string
	for _, p := range req.Projects {
		ok, err := newer(tx, "projects", p.UID, p.ModifiedAt)
		if err != nil {
			return resp, err
		}
		if !ok {
			rejectedProjects = append(rejectedProjects, p.UID)
			continue
		}
		rev++
		if _, err := tx.Exec(`INSERT OR REPLACE INTO projects(uid, name, parent_uid, position, is_inbox, deleted, created_at, modified_at, rev)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.UID, p.Name, p.ParentUID, p.Position, p.IsInbox, p.Deleted, p.CreatedAt, p.ModifiedAt, rev); err != nil {
			return resp, err
		}
	}
	for _, n := range req.Notes {
		ok, err := newer(tx, "notes", n.UID, n.ModifiedAt)
		if err != nil {
			return resp, err
		}
		if !ok {
			rejectedNotes = append(rejectedNotes, n.UID)
			continue
		}
		rev++
		if _, err := tx.Exec(`INSERT OR REPLACE INTO notes(uid, project_uid, body, color, priority, pinned, deleted, created_at, updated_at, modified_at, rev)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			n.UID, n.ProjectUID, n.Body, n.Color, n.Priority, n.Pinned, n.Deleted, n.CreatedAt, n.UpdatedAt, n.ModifiedAt, rev); err != nil {
			return resp, err
		}
	}
	if _, err := tx.Exec(`UPDATE meta SET value = ? WHERE key = 'rev'`, rev); err != nil {
		return resp, err
	}

	if resp.Projects, err = queryProjects(tx, req.Since, rejectedProjects); err != nil {
		return resp, err
	}
	if resp.Notes, err = queryNotes(tx, req.Since, rejectedNotes); err != nil {
		return resp, err
	}
	resp.Cursor = rev
	return resp, tx.Commit()
}

// newer reports whether a row with this change clock should replace the
// stored one: when there is none, or the stored one is older.
func newer(tx *sql.Tx, table, uid string, modified int64) (bool, error) {
	var stored int64
	err := tx.QueryRow(`SELECT modified_at FROM `+table+` WHERE uid = ?`, uid).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return err == nil && modified > stored, err
}

// changedSince selects rows with rev > since or a uid in extra, in rev order.
func changedSince(tx *sql.Tx, cols, table string, since int64, extra []string) (*sql.Rows, error) {
	q := `SELECT ` + cols + ` FROM ` + table + ` WHERE rev > ?`
	args := []any{since}
	if len(extra) > 0 {
		q += ` OR uid IN (SELECT value FROM json_each(?))`
		args = append(args, jsonStrings(extra))
	}
	return tx.Query(q+` ORDER BY rev`, args...)
}

func queryProjects(tx *sql.Tx, since int64, extra []string) ([]syncapi.Project, error) {
	rows, err := changedSince(tx, `uid, name, parent_uid, position, is_inbox, deleted, created_at, modified_at`, "projects", since, extra)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []syncapi.Project{}
	for rows.Next() {
		var p syncapi.Project
		if err := rows.Scan(&p.UID, &p.Name, &p.ParentUID, &p.Position, &p.IsInbox, &p.Deleted, &p.CreatedAt, &p.ModifiedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func queryNotes(tx *sql.Tx, since int64, extra []string) ([]syncapi.Note, error) {
	rows, err := changedSince(tx, `uid, project_uid, body, color, priority, pinned, deleted, created_at, updated_at, modified_at`, "notes", since, extra)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []syncapi.Note{}
	for rows.Next() {
		var n syncapi.Note
		if err := rows.Scan(&n.UID, &n.ProjectUID, &n.Body, &n.Color, &n.Priority, &n.Pinned, &n.Deleted,
			&n.CreatedAt, &n.UpdatedAt, &n.ModifiedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
func jsonStrings(s []string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
