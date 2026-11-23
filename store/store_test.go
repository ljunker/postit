package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInboxCreatedOnceAndProtected(t *testing.T) {
	s := open(t)
	a, _ := s.InboxID()
	b, _ := s.InboxID()
	if a != b {
		t.Fatalf("inbox ids differ: %d %d", a, b)
	}
	if err := s.DeleteProject(a); err != nil {
		t.Fatal(err)
	}
	_ = s.RenameProject(a, "x")
	ps, _ := s.ListProjects()
	if len(ps) != 1 || !ps[0].IsInbox || ps[0].Name != InboxName {
		t.Fatalf("inbox not intact: %+v", ps)
	}
}

func TestNoteOrderMoveAndCascade(t *testing.T) {
	s := open(t)
	p, err := s.CreateProject("Work")
	if err != nil {
		t.Fatal(err)
	}
	low, _ := s.CreateNote(p, "low", "")
	high, _ := s.CreateNote(p, "high\nmore", "blue")
	pinned, _ := s.CreateNote(p, "pinned", "")
	_ = s.SetPriority(high, 7) // clamped to 3
	_ = s.TogglePinned(pinned)

	ns, _ := s.ListNotes(p)
	if len(ns) != 3 || ns[0].ID != pinned || ns[1].ID != high || ns[2].ID != low {
		t.Fatalf("wrong order: %+v", ns)
	}
	if ns[1].Priority != MaxPriority || ns[1].Title() != "high" || ns[1].Color != "blue" {
		t.Fatalf("wrong note: %+v", ns[1])
	}

	inbox, _ := s.InboxID()
	if err := s.MoveNote(low, inbox); err != nil {
		t.Fatal(err)
	}
	if in, _ := s.ListNotes(inbox); len(in) != 1 || in[0].ID != low {
		t.Fatalf("move failed: %+v", in)
	}

	if err := s.DeleteProject(p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNote(high); err == nil {
		t.Fatal("note survived project delete")
	}
	if _, err := s.GetNote(low); err != nil {
		t.Fatal("moved note was deleted")
	}
}

func names(t *testing.T, s *Store) string {
	t.Helper()
	ps, err := s.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return strings.Join(out, ",")
}

func TestMoveProject(t *testing.T) {
	s := open(t)
	a, _ := s.CreateProject("A")
	s.CreateProject("B")
	c, _ := s.CreateProject("C")
	if got := names(t, s); got != "Inbox,A,B,C" {
		t.Fatalf("initial order: %s", got)
	}

	steps := []struct {
		id    int64
		index int
		want  string
	}{
		{c, 0, "Inbox,C,A,B"},
		{c, 99, "Inbox,A,B,C"}, // clamped to the end
		{a, 1, "Inbox,B,A,C"},
		{a, -5, "Inbox,A,B,C"}, // clamped to the top
	}
	for _, st := range steps {
		if err := s.MoveProject(st.id, st.index); err != nil {
			t.Fatal(err)
		}
		if got := names(t, s); got != st.want {
			t.Fatalf("move %d to %d: got %s, want %s", st.id, st.index, got, st.want)
		}
	}

	// A new project goes to the end.
	s.CreateProject("D")
	if got := names(t, s); got != "Inbox,A,B,C,D" {
		t.Fatalf("new project: %s", got)
	}

	// The inbox stays where it is.
	inbox, _ := s.InboxID()
	if err := s.MoveProject(inbox, 2); !errors.Is(err, ErrNotMovable) {
		t.Fatalf("moving the inbox: %v", err)
	}

	// Only the rows whose position changed go up with the next sync.
	if _, err := s.db.Exec(`UPDATE projects SET dirty = 0`); err != nil {
		t.Fatal(err)
	}
	before := s.Changes()
	if err := s.MoveProject(c, 3); err != nil { // swap C and D
		t.Fatal(err)
	}
	req, _ := s.Pending()
	if len(req.Projects) != 2 || s.Changes() == before {
		t.Fatalf("pending after swapping two: %+v", req.Projects)
	}
	before = s.Changes()
	if err := s.MoveProject(c, 3); err != nil { // already there
		t.Fatal(err)
	}
	if s.Changes() != before {
		t.Fatal("a no-op move counted as a change")
	}
}

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "\n\n  b  \nc": "b"} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}
