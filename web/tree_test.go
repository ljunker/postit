package web_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSubprojects(t *testing.T) {
	e := newEnv(t)
	b := e.login(t)
	for _, name := range []string{"A", "B", "C"} {
		b.post(t, "/p", url.Values{"name": {name}})
	}
	a, c := projIDByName(t, e.st, "A"), projIDByName(t, e.st, "C")
	cURL := "/p/" + itoa(c)

	_, page := b.get(t, cURL)
	if !strings.Contains(page, `action="`+cURL+`/parent"`) || !strings.Contains(page, `<option value="0" selected>`) {
		t.Fatal("parent form missing or top level not selected")
	}

	if resp := b.post(t, cURL+"/parent", url.Values{"parent": {itoa(a)}}); resp.StatusCode != http.StatusOK || resp.Request.URL.Path != cURL {
		t.Fatalf("set parent: %d at %s", resp.StatusCode, resp.Request.URL.Path)
	}
	if got := projectOrder(t, e.st); got != "Inbox,A,C,B" {
		t.Fatalf("after nesting: %s", got)
	}
	_, page = b.get(t, cURL)
	if !strings.Contains(page, `style="--depth:1"`) || !strings.Contains(page, `<option value="`+itoa(a)+`" selected>`) {
		t.Fatal("sidebar not indented or parent not selected")
	}

	// A cannot go under C, its own subproject, nor under itself or the inbox.
	aURL := "/p/" + itoa(a)
	for _, parent := range []string{itoa(c), itoa(a), itoa(inboxID(t, e.st)), "x"} {
		if resp := b.post(t, aURL+"/parent", url.Values{"parent": {parent}}); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("A under %s: %d", parent, resp.StatusCode)
		}
	}
	_, page = b.get(t, aURL)
	if strings.Contains(page, `<option value="`+itoa(c)+`"`) {
		t.Fatal("C offered as parent of A")
	}

	// Up/down only among siblings: C is A's only child.
	if _, cPage := b.get(t, cURL); strings.Contains(cPage, `name="dir" value="up"`) {
		t.Fatal("only child offers reordering")
	}

	// Back to the top level.
	b.post(t, cURL+"/parent", url.Values{"parent": {"0"}})
	if got := projectOrder(t, e.st); got != "Inbox,A,B,C" {
		t.Fatalf("back to top: %s", got)
	}
}
