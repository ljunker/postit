package server_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"postit/server"
	"postit/store"
	"postit/syncapi"
)

// A server database from before subprojects gets the column on open.
func TestServerMigratesParentColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE projects (uid TEXT PRIMARY KEY, name TEXT NOT NULL, position INTEGER NOT NULL,
		is_inbox INTEGER NOT NULL, deleted INTEGER NOT NULL, created_at INTEGER NOT NULL, modified_at INTEGER NOT NULL, rev INTEGER NOT NULL);
		INSERT INTO projects VALUES ('x', 'Old', 1, 0, 0, 1, 1, 1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 { // twice: the second open must not add the column again
		sdb, err := server.OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := sdb.Sync(syncapi.Request{})
		sdb.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.Projects) != 1 || resp.Projects[0].Name != "Old" || resp.Projects[0].ParentUID != "" {
			t.Fatalf("projects: %+v", resp.Projects)
		}
	}
}

func deviceTree(t *testing.T, st *store.Store) string {
	t.Helper()
	ps, err := st.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, strings.Repeat("-", p.Depth)+p.Name)
	}
	return strings.Join(out, ",")
}

// Nesting travels through the server, and when two devices nest two projects
// into each other while offline, both end up showing the same tree.
func TestSubprojectsSync(t *testing.T) {
	srv := newServer(t)
	a, b := newDevice(t, srv.URL), newDevice(t, srv.URL)

	for _, name := range []string{"Work", "Client", "Home"} {
		if _, err := a.st.CreateProject(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.st.SetParent(projectID(t, a.st, "Client"), projectID(t, a.st, "Work")); err != nil {
		t.Fatal(err)
	}
	a.sync(t)
	b.sync(t)
	if got := deviceTree(t, b.st); got != "Inbox,Work,-Client,Home" {
		t.Fatalf("tree on b: %s", got)
	}

	// Offline on both: a puts Work under Home, b puts Home under Client.
	if err := a.st.SetParent(projectID(t, a.st, "Work"), projectID(t, a.st, "Home")); err != nil {
		t.Fatal(err)
	}
	if err := b.st.SetParent(projectID(t, b.st, "Home"), projectID(t, b.st, "Client")); err != nil {
		t.Fatal(err)
	}
	a.sync(t)
	b.sync(t)
	a.sync(t)
	ta, tb := deviceTree(t, a.st), deviceTree(t, b.st)
	if ta != tb {
		t.Fatalf("devices disagree: %s vs %s", ta, tb)
	}
	if strings.Count(ta, ",") != 3 {
		t.Fatalf("a project went missing: %s", ta)
	}

	// Deleting a parent on one device lifts its children on the other.
	if err := a.st.SetParent(projectID(t, a.st, "Home"), 0); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetParent(projectID(t, a.st, "Work"), 0); err != nil {
		t.Fatal(err)
	}
	if err := a.st.SetParent(projectID(t, a.st, "Client"), projectID(t, a.st, "Work")); err != nil {
		t.Fatal(err)
	}
	if err := a.st.DeleteProject(projectID(t, a.st, "Work")); err != nil {
		t.Fatal(err)
	}
	a.sync(t)
	b.sync(t)
	if ta, tb := deviceTree(t, a.st), deviceTree(t, b.st); ta != tb || strings.Contains(ta, "Work") || strings.Contains(ta, "-") {
		t.Fatalf("after delete: a=%s b=%s", ta, tb)
	}
}
