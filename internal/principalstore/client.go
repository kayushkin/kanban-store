// Package principalstore is kanban-store's client for principal-store, the
// registry that owns who a principal is. It exists for exactly one call:
// checking, before a card assignment is written, that the principal being
// assigned exists and is not disabled.
//
// kanban-store is otherwise deliberately dumb. It never resolves a card link's
// entity_ref, never proxies to the service an entity belongs to, and publishes
// /api/entity-types precisely so that clients resolve refs themselves. Card
// assignment is the one exception, and it is an exception on purpose. A link to
// a session that does not exist is a dangling pointer a reader can notice and
// discard; an assignment to a principal that does not exist is a silently wrong
// row forever — nothing downstream re-checks who is assigned, the reverse
// lookup (GET /api/assignments?principal_id=) would report work for someone who
// is not there, and the card would look owned when it is not. The check is one
// GET with a short timeout, and a failed check fails the write: an unreachable
// principal-store is a 502, never a silently accepted row.
package principalstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one principal-store.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a client with the 3-second timeout the assignment write path
// budgets for the existence check.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 3 * time.Second},
	}
}

// Principal is the part of principal-store's record the existence check reads.
// DisabledAt is principal-store's unix-seconds stamp; 0 means the principal is
// active, and principal-store never deletes a row, so this is the only removal.
type Principal struct {
	ID         string `json:"id"`
	DisabledAt int64  `json:"disabled_at"`
}

// Disabled reports whether principal-store has retired this principal.
func (p *Principal) Disabled() bool { return p.DisabledAt != 0 }

// ErrNotFound is returned when principal-store answers 404 for the id.
var ErrNotFound = errors.New("principal not found in principal-store")

// Get fetches one principal by id. A 404 is ErrNotFound. A transport failure
// (unreachable, timeout) is returned as the transport's own error, unwrapped,
// and any other non-200 answer as an error naming the status and body, so a
// caller can surface exactly what went wrong instead of a paraphrase.
func (c *Client) Get(id string) (*Principal, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/principals/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("principal-store GET %s: %s — %s", req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	var p Principal
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("principal-store GET %s: undecodable body: %w", req.URL.Path, err)
	}
	return &p, nil
}
