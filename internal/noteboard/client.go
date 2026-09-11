// Package noteboard is a minimal HTTP client for the upstream noteboard
// service. It treats noteboard items as opaque JSON (map[string]any) so we
// pass through fields without redefining noteboard's schema in this repo
// (per CLAUDE.md: layers are transparent).
package noteboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Item is opaque — we don't redefine noteboard's full shape here.
type Item = map[string]any

type CreateItemPayload struct {
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	Body     *string  `json:"body,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Priority *int     `json:"priority,omitempty"`
	ListID   *string  `json:"list_id,omitempty"`
	DueAt    *string  `json:"due_at,omitempty"`
	// ParentID is the canonical parent edge (noteboard item -> item). The hold
	// gate and the spend ceiling both roll up over it: a held parent withholds its
	// children, and a parent's ceiling covers the whole tree beneath it. A sub-card
	// created without it is a sub-card that escapes both.
	ParentID *string `json:"parent_id,omitempty"`
	// Hold creates the card already parked, so no agent can pick the work up in
	// the gap between the card appearing and a human getting to the board.
	Hold       bool   `json:"hold,omitempty"`
	HoldReason string `json:"hold_reason,omitempty"`
	// AutoHoldAtUSD is the spend ceiling. Pointer, because nil (no ceiling) and
	// 0 (stop before spending a cent) are different instructions.
	AutoHoldAtUSD *float64 `json:"auto_hold_at_usd,omitempty"`
}

func (c *Client) CreateItem(p CreateItemPayload) (Item, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest("POST", c.BaseURL+"/api/items", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, http.StatusCreated)
}

func (c *Client) GetItem(id string) (Item, error) {
	req, _ := http.NewRequest("GET", c.BaseURL+"/api/items/"+url.PathEscape(id), nil)
	return c.doJSON(req, http.StatusOK)
}

func (c *Client) PatchItem(id string, patch map[string]any) (Item, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest("PATCH", c.BaseURL+"/api/items/"+url.PathEscape(id), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, http.StatusOK)
}

// HoldItem parks the card's work: agents stop seeing it, the card stays on the
// board. Reason is optional.
func (c *Client) HoldItem(id, reason string) (Item, error) {
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest("POST", c.BaseURL+"/api/items/"+url.PathEscape(id)+"/hold", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, http.StatusOK)
}

// UnholdItem clears the gate — agents may pick the work up again.
func (c *Client) UnholdItem(id string) (Item, error) {
	req, _ := http.NewRequest("POST", c.BaseURL+"/api/items/"+url.PathEscape(id)+"/unhold", nil)
	return c.doJSON(req, http.StatusOK)
}

// DeleteItem asks noteboard for a reversible delete by default: the item is
// stamped deleted_at and drops out of every read path, but the row survives and
// can be restored. It is NOT archiving — the item's status is left untouched.
// Pass hard=true to purge the row permanently.
func (c *Client) DeleteItem(id string, hard bool) error {
	u := c.BaseURL + "/api/items/" + url.PathEscape(id)
	if hard {
		u += "?hard=true"
	}
	req, _ := http.NewRequest("DELETE", u, nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("noteboard delete %s: %s", id, resp.Status)
	}
	return nil
}

// GetItems fetches a batch of items by id. noteboard has no batch endpoint
// today, so we fan out concurrent GETs (capped) and return results in input
// order. Items that 404 are returned as nil entries — callers decide whether
// to treat them as orphans.
func (c *Client) GetItems(ids []string) ([]Item, error) {
	out := make([]Item, len(ids))
	errs := make([]error, len(ids))
	const concurrency = 8
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			item, err := c.GetItem(id)
			if err != nil {
				if isNotFound(err) {
					out[i] = nil
					return
				}
				errs[i] = err
				return
			}
			out[i] = item
		}(i, id)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return out, e
		}
	}
	return out, nil
}

// Search forwards a full-text query to noteboard /api/search.
//
// include_held is set because a kanban board is a management surface, not a
// discovery one: the board is where a human goes to find parked work and resume
// it. Noteboard withholds held items from agent discovery by default, and a card
// that vanished from the board it is parked on could never be un-parked.
func (c *Client) Search(q string, limit int) ([]Item, error) {
	u := fmt.Sprintf("%s/api/search?q=%s&include_held=true", c.BaseURL, url.QueryEscape(q))
	if limit > 0 {
		u += "&limit=" + strconv.Itoa(limit)
	}
	req, _ := http.NewRequest("GET", u, nil)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("noteboard search: %s", resp.Status)
	}
	var out []Item
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// notFoundError lets callers distinguish missing items from real failures.
type notFoundError struct{ id string }

func (e *notFoundError) Error() string { return "noteboard item not found: " + e.id }

// IsNotFound reports whether err is noteboard answering that the item does
// not exist, as opposed to noteboard failing to answer.
func IsNotFound(err error) bool { return isNotFound(err) }

func isNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

func (c *Client) doJSON(req *http.Request, want int) (Item, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// best-effort id from URL path tail
		id := req.URL.Path
		return nil, &notFoundError{id: id}
	}
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("noteboard %s %s: %s — %s", req.Method, req.URL.Path, resp.Status, string(body))
	}
	var item Item
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, err
	}
	return item, nil
}
