package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"postit/syncapi"
)

// An old database without sync columns keeps its data and gets uids, with
// the inbox on the fixed uid and everything marked for upload.
func TestMigrateOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(baseSchema + `
		INSERT INTO projects(id, name, position, is_inbox, created_at) VALUES (1, 'Inbox', -1, 1, 1), (2, 'Work', 1, 0, 2);
		INSERT INTO notes(project_id, body, created_at, updated_at) VALUES (1, 'a', 3, 3), (2, 'b', 4, 4);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ps, _ := s.ListProjects()
	if len(ps) != 2 || ps[0].NoteCount != 1 || ps[1].Name != "Work" {
		t.Fatalf("projects after migration: %+v", ps)
	}
	req, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Projects) != 2 || len(req.Notes) != 2 {
		t.Fatalf("pending after migration: %+v", req)
	}
	uids := map[string]bool{}
	for _, p := range req.Projects {
		uids[p.UID] = true
		if p.IsInbox != (p.UID == syncapi.InboxUID) {
			t.Fatalf("inbox uid wrong: %+v", p)
		}
	}
	for _, n := range req.Notes {
		if uids[n.UID] || n.UID == "" || !uids[n.ProjectUID] || n.ModifiedAt != n.UpdatedAt {
			t.Fatalf("note not migrated: %+v", n)
		}
	}

	// Opening again does not migrate twice.
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestChangesAreTrackedAndDeletesSoft(t *testing.T) {
	s := open(t)
	settle(t, s)
	before := s.Changes()

	p, _ := s.CreateProject("Work")
	n, _ := s.CreateNote(p, "x", "")
	settle(t, s)
	_ = s.SetColor(n, "blue")
	req, _ := s.Pending()
	if len(req.Projects) != 0 || len(req.Notes) != 1 || req.Notes[0].Color != "blue" {
		t.Fatalf("pending after SetColor: %+v", req)
	}
	if s.Changes() == before {
		t.Fatal("changes not counted")
	}

	settle(t, s)
	_ = s.DeleteProject(p)
	req, _ = s.Pending()
	if len(req.Projects) != 1 || !req.Projects[0].Deleted || len(req.Notes) != 1 || !req.Notes[0].Deleted {
		t.Fatalf("delete not sent as tombstones: %+v", req)
	}
	if ps, _ := s.ListProjects(); len(ps) != 1 {
		t.Fatalf("deleted project still listed: %+v", ps)
	}
}

func TestApplyRemoteLastWriteWins(t *testing.T) {
	s := open(t)
	p, _ := s.CreateProject("Work")
	id, _ := s.CreateNote(p, "local", "")
	req, _ := s.Pending()
	var note syncapi.Note
	for _, n := range req.Notes {
		note = n
	}

	// An older remote version does not overwrite an unsent local change.
	old := note
	old.Body, old.ModifiedAt = "older remote", note.ModifiedAt-1
	changed, err := s.ApplyRemote(syncapi.Response{Cursor: 5, Notes: []syncapi.Note{old}})
	if err != nil || changed {
		t.Fatalf("older remote applied: changed=%v err=%v", changed, err)
	}
	if got, _ := s.GetNote(id); got.Body != "local" {
		t.Fatalf("local change lost: %q", got.Body)
	}
	if c, _ := s.Cursor(); c != 5 {
		t.Fatalf("cursor = %d", c)
	}

	// Our own row coming back clears the dirty flag without a change.
	changed, _ = s.ApplyRemote(syncapi.Response{Cursor: 6, Projects: req.Projects, Notes: req.Notes})
	if pending, _ := s.HasPending(); pending || changed {
		t.Fatalf("echo: pending=%v changed=%v", pending, changed)
	}

	// A newer remote version wins, also for a new note in an unknown project.
	newer := note
	newer.Body, newer.ModifiedAt = "remote", note.ModifiedAt+10
	stray := syncapi.Note{UID: "n2", ProjectUID: "unknown", Body: "stray", ModifiedAt: 1}
	changed, _ = s.ApplyRemote(syncapi.Response{Cursor: 7, Notes: []syncapi.Note{newer, stray}})
	if got, _ := s.GetNote(id); !changed || got.Body != "remote" {
		t.Fatalf("newer remote not applied: %q", got.Body)
	}
	inbox, _ := s.InboxID()
	if ns, _ := s.ListNotes(inbox); len(ns) != 1 || ns[0].Body != "stray" || ns[0].Color != "yellow" {
		t.Fatalf("stray note not in inbox: %+v", ns)
	}
}

// settle marks everything as sent, like a successful sync would.
func settle(t *testing.T, s *Store) {
	t.Helper()
	req, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyRemote(syncapi.Response{Projects: req.Projects, Notes: req.Notes}); err != nil {
		t.Fatal(err)
	}
}
