// Package syncapi holds the wire types of the sync protocol shared by the
// p client and the postitd server.
//
// A client posts its changed rows together with the cursor of its last sync to
// POST /api/v1/sync. The server keeps, per row, the version with the newest
// ModifiedAt (last write wins), and answers with every row that changed since
// the cursor plus its own version of every pushed row it rejected.
package syncapi

// Path is the sync endpoint.
const Path = "/api/v1/sync"

// PingPath answers 204 when the token is valid.
const PingPath = "/api/v1/ping"

// InboxPath takes a plain-text body and files it as a new post-it in the
// inbox (for clients that cannot speak the sync protocol, e.g. iOS Shortcuts).
// It answers 201 with the new note as JSON.
const InboxPath = "/api/v1/inbox"

// InboxUID is the fixed uid of the inbox project on every device.
const InboxUID = "inbox"

type Project struct {
	UID        string `json:"uid"`
	Name       string `json:"name"`
	Position   int64  `json:"position"`
	IsInbox    bool   `json:"is_inbox"`
	Deleted    bool   `json:"deleted"`
	CreatedAt  int64  `json:"created_at"`  // UnixNano
	ModifiedAt int64  `json:"modified_at"` // UnixNano, last change of any field
}

type Note struct {
	UID        string `json:"uid"`
	ProjectUID string `json:"project_uid"`
	Body       string `json:"body"`
	Color      string `json:"color"`
	Priority   int    `json:"priority"`
	Pinned     bool   `json:"pinned"`
	Deleted    bool   `json:"deleted"`
	CreatedAt  int64  `json:"created_at"`  // UnixNano
	UpdatedAt  int64  `json:"updated_at"`  // UnixNano, last change of the body (sort order)
	ModifiedAt int64  `json:"modified_at"` // UnixNano, last change of any field
}

type Request struct {
	Since    int64     `json:"since"`
	Projects []Project `json:"projects"`
	Notes    []Note    `json:"notes"`
}

type Response struct {
	Cursor   int64     `json:"cursor"`
	Projects []Project `json:"projects"`
	Notes    []Note    `json:"notes"`
}

// Error is the body of every non-2xx answer.
type Error struct {
	Error string `json:"error"`
}
