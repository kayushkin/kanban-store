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

// DeleteItem soft-archives by default; pass hard=true for permanent delete.
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
func (c *Client) Search(q string, limit int) ([]Item, error) {
	u := fmt.Sprintf("%s/api/search?q=%s", c.BaseURL, url.QueryEscape(q))
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
