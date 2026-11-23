package store

import (
	"database/sql"
	"errors"

	"postit/syncapi"
)

// Pending returns the cursor of the last sync and every local change not yet
// sent to the server, ready to post.
func (s *Store) Pending() (syncapi.Request, error) {
	req := syncapi.Request{Projects: []syncapi.Project{}, Notes: []syncapi.Note{}}
	var err error
	if req.Since, err = s.Cursor(); err != nil {
		return req, err
	}

	rows, err := s.db.Query(`
		SELECT uid, name, parent_uid, position, is_inbox, deleted, created_at, modified_at
		FROM projects WHERE dirty = 1`)
	if err != nil {
		return req, err
	}
	for rows.Next() {
		var p syncapi.Project
		if err := rows.Scan(&p.UID, &p.Name, &p.ParentUID, &p.Position, &p.IsInbox, &p.Deleted, &p.CreatedAt, &p.ModifiedAt); err != nil {
			rows.Close()
			return req, err
		}
		req.Projects = append(req.Projects, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return req, err
	}

	rows, err = s.db.Query(`
		SELECT n.uid, p.uid, n.body, n.color, n.priority, n.pinned, n.deleted, n.created_at, n.updated_at, n.modified_at
		FROM notes n JOIN projects p ON p.id = n.project_id
		WHERE n.dirty = 1`)
	if err != nil {
		return req, err
	}
	defer rows.Close()
	for rows.Next() {
		var n syncapi.Note
		if err := rows.Scan(&n.UID, &n.ProjectUID, &n.Body, &n.Color, &n.Priority, &n.Pinned, &n.Deleted,
			&n.CreatedAt, &n.UpdatedAt, &n.ModifiedAt); err != nil {
			return req, err
		}
		req.Notes = append(req.Notes, n)
	}
	return req, rows.Err()
}

// HasPending reports whether there are local changes not yet sent.
func (s *Store) HasPending() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM projects WHERE dirty = 1) + (SELECT COUNT(*) FROM notes WHERE dirty = 1)`).Scan(&n)
	return n > 0, err
}

// Cursor is the server revision of the last successful sync (0 = never).
func (s *Store) Cursor() (int64, error) {
	var c int64
	err := s.db.QueryRow(`SELECT value FROM sync_state WHERE key = 'cursor'`).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return c, err
}

// ApplyRemote merges the server's answer and stores its cursor, all in one
// transaction. A remote row replaces the local one unless the local row has a
// newer unsent change. The rows we pushed come back from the server, so this
// also clears their dirty flag. changed reports whether anything visible
// differs from before (i.e. the UI should reload).
func (s *Store) ApplyRemote(resp syncapi.Response) (changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	for _, p := range resp.Projects {
		c, err := applyProject(tx, p)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	for _, n := range resp.Notes {
		c, err := applyNote(tx, n)
		if err != nil {
			return false, err
		}
		changed = changed || c
	}
	if _, err := tx.Exec(`INSERT INTO sync_state(key, value) VALUES ('cursor', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, resp.Cursor); err != nil {
		return false, err
	}
	return changed, tx.Commit()
}

// local reads a row's id, change clock and dirty flag by uid.
func local(tx *sql.Tx, table, uid string) (id, modified int64, dirty, found bool, err error) {
	err = tx.QueryRow(`SELECT id, modified_at, dirty FROM `+table+` WHERE uid = ?`, uid).Scan(&id, &modified, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, false, nil
	}
	return id, modified, dirty, err == nil, err
}

func applyProject(tx *sql.Tx, p syncapi.Project) (bool, error) {
	id, modified, dirty, found, err := local(tx, "projects", p.UID)
	if err != nil {
		return false, err
	}
	if found && dirty && modified > p.ModifiedAt {
		return false, nil // our newer change goes up next time
	}
	if !found {
		_, err := tx.Exec(`INSERT INTO projects(uid, name, parent_uid, position, is_inbox, deleted, created_at, modified_at, dirty)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			p.UID, p.Name, p.ParentUID, p.Position, p.IsInbox, p.Deleted, p.CreatedAt, p.ModifiedAt)
		return !p.Deleted, err
	}
	_, err = tx.Exec(`UPDATE projects SET name = ?, parent_uid = ?, position = ?, is_inbox = ?, deleted = ?, created_at = ?, modified_at = ?, dirty = 0
		WHERE id = ?`, p.Name, p.ParentUID, p.Position, p.IsInbox, p.Deleted, p.CreatedAt, p.ModifiedAt, id)
	return modified != p.ModifiedAt, err
}

func applyNote(tx *sql.Tx, n syncapi.Note) (bool, error) {
	id, modified, dirty, found, err := local(tx, "notes", n.UID)
	if err != nil {
		return false, err
	}
	if found && dirty && modified > n.ModifiedAt {
		return false, nil
	}
	// The project normally arrives in the same answer or earlier; if it is
	// unknown anyway, keep the note visible in the inbox.
	var projectID int64
	err = tx.QueryRow(`SELECT id FROM projects WHERE uid = ?`, n.ProjectUID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`SELECT id FROM projects WHERE uid = ?`, syncapi.InboxUID).Scan(&projectID)
	}
	if err != nil {
		return false, err
	}
	if n.Color == "" {
		n.Color = "yellow"
	}
	if !found {
		_, err := tx.Exec(`INSERT INTO notes(uid, project_id, body, color, priority, pinned, deleted, created_at, updated_at, modified_at, dirty)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			n.UID, projectID, n.Body, n.Color, clampPrio(n.Priority), n.Pinned, n.Deleted, n.CreatedAt, n.UpdatedAt, n.ModifiedAt)
		return !n.Deleted, err
	}
	_, err = tx.Exec(`UPDATE notes SET project_id = ?, body = ?, color = ?, priority = ?, pinned = ?, deleted = ?,
		created_at = ?, updated_at = ?, modified_at = ?, dirty = 0 WHERE id = ?`,
		projectID, n.Body, n.Color, clampPrio(n.Priority), n.Pinned, n.Deleted, n.CreatedAt, n.UpdatedAt, n.ModifiedAt, id)
	return modified != n.ModifiedAt, err
}
