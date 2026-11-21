package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"postit/store"
	"postit/syncapi"
)

// ErrUnauthorized means the server rejected the token.
var ErrUnauthorized = errors.New("Token ungültig")

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

// Ping checks that the server is reachable and accepts the token.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.Server+syncapi.PingPath, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Result describes one sync.
type Result struct {
	Pushed  int  // rows sent
	Pulled  int  // rows received
	Changed bool // something visible changed locally
}

// Sync sends local changes and merges the server's.
func (c *Client) Sync(ctx context.Context, st *store.Store) (Result, error) {
	var res Result
	payload, err := st.Pending()
	if err != nil {
		return res, err
	}
	res.Pushed = len(payload.Projects) + len(payload.Notes)
	body, err := json.Marshal(payload)
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Server+syncapi.Path, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	var answer syncapi.Response
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return res, fmt.Errorf("Antwort des Servers: %w", err)
	}
	res.Pulled = len(answer.Projects) + len(answer.Notes)
	res.Changed, err = st.ApplyRemote(answer)
	return res, err
}

// do sends req with the token and turns non-2xx answers into errors.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("User-Agent", "postit-p")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	var e syncapi.Error
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(b, &e) != nil || e.Error == "" {
		e.Error = string(bytes.TrimSpace(b))
	}
	return nil, fmt.Errorf("Server: %s: %s", resp.Status, e.Error)
}
