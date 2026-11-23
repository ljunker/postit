package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// tree lists the projects in display order, each prefixed with one "-" per level.
func tree(t *testing.T, s *Store) string {
	t.Helper()
	ps, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, strings.Repeat("-", p.Depth)+p.Name)
	}
	return strings.Join(out, ",")
}

func mustCreate(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.CreateProject(name)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustParent(t *testing.T, s *Store, id, parent int64) {
	t.Helper()
	if err := s.SetParent(id, parent); err != nil {
		t.Fatal(err)
	}
}

func TestSubprojects(t *testing.T) {
	s := open(t)
	a := mustCreate(t, s, "A")
	b := mustCreate(t, s, "B")
	c := mustCreate(t, s, "C")
	d := mustCreate(t, s, "D")

	mustParent(t, s, c, a)
	mustParent(t, s, b, a)
	mustParent(t, s, d, c)
	if got := tree(t, s); got != "Inbox,A,-C,--D,-B" {
		t.Fatalf("tree: %s", got)
	}
	ps, _ := s.ListProjects()
	if ps[3].ParentID != c || ps[1].ParentID != 0 {
		t.Fatalf("parent ids: %+v", ps)
	}

	// Rejected: itself, a subproject, the inbox (as child or parent), unknown.
	inbox, _ := s.InboxID()
	for _, tc := range []struct {
		id, parent int64
		want       error
	}{
		{a, a, ErrCycle},
		{a, d, ErrCycle},
		{a, inbox, ErrCycle},
		{a, 999, ErrCycle},
		{inbox, a, ErrNotMovable},
	} {
		if err := s.SetParent(tc.id, tc.parent); !errors.Is(err, tc.want) {
			t.Fatalf("SetParent(%d, %d) = %v, want %v", tc.id, tc.parent, err, tc.want)
		}
	}
	if got := tree(t, s); got != "Inbox,A,-C,--D,-B" {
		t.Fatalf("tree after rejected moves: %s", got)
	}

	// Reordering stays among siblings, the subtree moves along.
	if err := s.MoveProject(b, 0); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, s); got != "Inbox,A,-B,-C,--D" {
		t.Fatalf("after moving B first: %s", got)
	}
	if i, n := SiblingIndex(ps, d); i != 0 || n != 1 {
		t.Fatalf("SiblingIndex(D) = %d, %d", i, n)
	}

	// Back to the top level: last there.
	mustParent(t, s, c, 0)
	if got := tree(t, s); got != "Inbox,A,-B,C,-D" {
		t.Fatalf("C to top: %s", got)
	}

	// Deleting a project lifts its subprojects one level, after its siblings.
	e := mustCreate(t, s, "E")
	mustParent(t, s, e, a)
	mustParent(t, s, c, a) // A: B, E, C(D)
	if got := tree(t, s); got != "Inbox,A,-B,-E,-C,--D" {
		t.Fatalf("before delete: %s", got)
	}
	nid, _ := s.CreateNote(a, "in A", "")
	if err := s.DeleteProject(a); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, s); got != "Inbox,B,E,C,-D" {
		t.Fatalf("after deleting A: %s", got)
	}
	if _, err := s.GetNote(nid); err == nil {
		t.Fatal("A's note survived")
	}
	if err := s.DeleteProject(c); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, s); got != "Inbox,B,E,D" {
		t.Fatalf("after deleting C: %s", got)
	}
}

// Orphans and cycles (possible after two devices synced conflicting moves)
// show at the top level, in a stable order.
func TestBrokenTreeShownAtTop(t *testing.T) {
	s := open(t)
	mustCreate(t, s, "A")
	mustCreate(t, s, "B")
	mustCreate(t, s, "C")
	mustCreate(t, s, "D")
	if _, err := s.db.Exec(`
		UPDATE projects SET parent_uid = (SELECT uid FROM projects WHERE name = 'B') WHERE name = 'A';
		UPDATE projects SET parent_uid = (SELECT uid FROM projects WHERE name = 'A') WHERE name = 'B';
		UPDATE projects SET parent_uid = 'gone' WHERE name = 'C';
		UPDATE projects SET parent_uid = 'inbox' WHERE name = 'D';`); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, s); got != "Inbox,C,D,A,-B" {
		t.Fatalf("broken tree: %s", got)
	}
	// The cycle can be untangled from there: B goes to the top, and A, still
	// stored under B, follows it.
	b := projectByName(t, s, "B")
	mustParent(t, s, b, 0)
	if got := tree(t, s); got != "Inbox,C,D,B,-A" {
		t.Fatalf("after lifting B: %s", got)
	}
}

func projectByName(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	ps, _ := s.ListProjects()
	for _, p := range ps {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no project %q", name)
	return 0
}

// A database from before subprojects gets the column and keeps its order.
func TestMigrateV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "A")
	mustCreate(t, s, "B")
	s.Close()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE projects DROP COLUMN parent_uid; PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := tree(t, s); got != "Inbox,A,B" {
		t.Fatalf("after migration: %s", got)
	}
	var v int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 2 {
		t.Fatalf("user_version = %d", v)
	}
	mustParent(t, s, projectByName(t, s, "B"), projectByName(t, s, "A"))
	if got := tree(t, s); got != "Inbox,A,-B" {
		t.Fatalf("nesting after migration: %s", got)
	}
}
