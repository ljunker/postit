package store

import (
	"path/filepath"
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

func TestTitle(t *testing.T) {
	for in, want := range map[string]string{"": "", "a": "a", "\n\n  b  \nc": "b"} {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}
