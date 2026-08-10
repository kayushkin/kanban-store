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
	return c.doJSON(req, http.StatusCreated, "")
}

func (c *Client) GetItem(id string) (Item, error) {
	req, _ := http.NewRequest("GET", c.BaseURL+"/api/items/"+url.PathEscape(id), nil)
	return c.doJSON(req, http.StatusOK, id)
}

func (c *Client) PatchItem(id string, patch map[string]any) (Item, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest("PATCH", c.BaseURL+"/api/items/"+url.PathEscape(id), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, http.StatusOK, id)
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
	return c.doJSON(req, http.StatusOK, id)
}

// UnholdItem clears the gate — agents may pick the work up again.
func (c *Client) UnholdItem(id string) (Item, error) {
	req, _ := http.NewRequest("POST", c.BaseURL+"/api/items/"+url.PathEscape(id)+"/unhold", nil)
	return c.doJSON(req, http.StatusOK, id)
}

// DeleteItem removes the item reversibly by default; pass hard=true to purge.
//
// ⚠️ A reversible delete is NOT an archive, and reading it as one is the
// expensive mistake on this route. noteboard stamps deleted_at and leaves
// `status` exactly as it was — an open card stays "open" — while dropping the
// row out of every read path, so a later GET answers 404. Deleting is the item
// being taken away; archiving is a state the user chose for a live item, and
// collapsing the two means a restore cannot tell them apart.
//
// The consequence for this repo: a soft-deleted card's placement survives and
// its item read 404s, so the board reports it as an orphan. Nothing anywhere
// sets status to "archived".
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

func isNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

// doJSON is the shared transport under every call that reads an item back. The
// itemID names which item the request is about, and "" means the request is
// about no particular item yet (a create). That distinction decides what a 404
// means: for a named item it is "this item is not visible", which callers turn
// into an orphan; with no item named it is "noteboard does not serve this
// route", which is a real failure and must not be reported as a missing card.
func (c *Client) doJSON(req *http.Request, want int, itemID string) (Item, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && itemID != "" {
		return nil, &notFoundError{id: itemID}
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
