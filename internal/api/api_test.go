package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kayushkin/kanban-store/internal/api"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/noteboard"
	"github.com/kayushkin/kanban-store/internal/principalstore"
)

// ============================ Test harness ============================

// fakeNoteboard is an in-memory stand-in for the upstream noteboard service.
// kanban-store's API treats noteboard as the source of truth for card content,
// so the tests need a stateful backend that creates items, applies PATCHes
// (this is how auto_status is observed), serves GETs for board-view assembly,
// and 404s on missing items. We mount it on an httptest.Server and point the
// real noteboard.Client at it — no network, no real noteboard required.
type fakeNoteboard struct {
	mu    sync.Mutex
	items map[string]map[string]any
	seq   int
}

func newFakeNoteboard() *fakeNoteboard {
	return &fakeNoteboard{items: map[string]map[string]any{}}
}

// setHold parks or releases an item the way noteboard does: held_at is a
// timestamp that is either there or not, and it is deliberately not the item's
// status — held work is still open work, it just is not cleared to run.
func (f *fakeNoteboard) setHold(w http.ResponseWriter, id string, held bool, reason string) {
	f.mu.Lock()
	it, ok := f.items[id]
	var out map[string]any
	if ok {
		if held {
			it["held_at"] = time.Now().UTC().Format(time.RFC3339)
			it["hold_reason"] = reason
		} else {
			delete(it, "held_at")
			delete(it, "hold_reason")
		}
		out = clone(it)
	}
	f.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, out)
}

func (f *fakeNoteboard) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/items", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		f.mu.Lock()
		f.seq++
		id := "nb-" + itoa(f.seq)
		payload["id"] = id
		if _, ok := payload["status"]; !ok {
			payload["status"] = "open"
		}
		f.items[id] = payload
		out := clone(payload)
		f.mu.Unlock()
		writeJSON(w, 201, out)
	})

	mux.HandleFunc("GET /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		it, ok := f.items[id]
		var out map[string]any
		if ok {
			out = clone(it)
		}
		f.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("PATCH /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var patch map[string]any
		json.NewDecoder(r.Body).Decode(&patch)
		f.mu.Lock()
		it, ok := f.items[id]
		var out map[string]any
		if ok {
			for k, v := range patch {
				it[k] = v
			}
			out = clone(it)
		}
		f.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("DELETE /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		hard := r.URL.Query().Get("hard") == "true"
		f.mu.Lock()
		it, ok := f.items[id]
		if ok {
			if hard {
				delete(f.items, id)
			} else {
				it["status"] = "archived"
			}
		}
		f.mu.Unlock()
		if !ok {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})

	// The stop/play button. kanban-store forwards a hold straight through to
	// noteboard, so the fake has to hold state for it or every clock event that
	// follows a hold goes untested.
	mux.HandleFunc("POST /api/items/{id}/hold", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.setHold(w, r.PathValue("id"), true, req.Reason)
	})

	mux.HandleFunc("POST /api/items/{id}/unhold", func(w http.ResponseWriter, r *http.Request) {
		f.setHold(w, r.PathValue("id"), false, "")
	})

	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		f.mu.Lock()
		var out []map[string]any
		for _, it := range f.items {
			title, _ := it["title"].(string)
			if q == "" || strings.Contains(strings.ToLower(title), q) {
				out = append(out, clone(it))
			}
		}
		f.mu.Unlock()
		writeJSON(w, 200, out)
	})

	return mux
}

// status returns the stored status for an item, or "" if it does not exist.
// Used to assert auto_status was applied to the upstream noteboard item.
func (f *fakeNoteboard) status(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if it, ok := f.items[id]; ok {
		s, _ := it["status"].(string)
		return s
	}
	return ""
}

// seedItem inserts an item directly, simulating a noteboard item that exists
// independently of kanban (used to test attaching an existing card).
func (f *fakeNoteboard) seedItem(title string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := "nb-" + itoa(f.seq)
	f.items[id] = map[string]any{"id": id, "title": title, "status": "open"}
	return id
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// setup builds an API backed by a fresh temp SQLite store, an in-memory fake
// noteboard and a stub principal-store (see assignments_test.go for what it
// knows). The returned cleanup tears all three down.
func setup(t *testing.T) (http.Handler, *fakeNoteboard, func()) {
	t.Helper()
	principals := httptest.NewServer(newFakePrincipalStore().handler())
	h, nb, cleanup := setupAgainstPrincipalStore(t, principals.URL)
	return h, nb, func() {
		cleanup()
		principals.Close()
	}
}

// setupAgainstPrincipalStore is setup with the principal-store URL chosen by
// the test — pointed at a closed port to exercise the unreachable path.
func setupAgainstPrincipalStore(t *testing.T, principalStoreURL string) (http.Handler, *fakeNoteboard, func()) {
	t.Helper()
	h, nb, _, cleanup := setupExposingStore(t, principalStoreURL)
	return h, nb, cleanup
}

// setupExposingStore also hands back the store itself, for the tests that need
// to build state no HTTP route will — a placement with no event behind it, the
// shape of every card that predates the event log.
func setupExposingStore(t *testing.T, principalStoreURL string) (http.Handler, *fakeNoteboard, *db.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	nb := newFakeNoteboard()
	srv := httptest.NewServer(nb.handler())
	a := api.New(store, noteboard.New(srv.URL), principalstore.New(principalStoreURL))
	return a.Handler(), nb, store, func() {
		srv.Close()
		store.Close()
	}
}

// do issues a request against the handler and returns the recorder. body may be
// nil (no request body) or any value (JSON-encoded).
func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
}

// mkBoard creates a board and returns its id.
func mkBoard(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	w := do(t, h, "POST", "/api/boards", model.CreateBoardRequest{Name: name})
	if w.Code != 201 {
		t.Fatalf("create board: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var b model.Board
	decode(t, w, &b)
	return b.ID
}

// mkColumn creates a column on a board and returns its id. autoStatus "" leaves
// it unset.
func mkColumn(t *testing.T, h http.Handler, boardID, name, autoStatus string) string {
	t.Helper()
	req := model.CreateColumnRequest{Name: name}
	if autoStatus != "" {
		req.AutoStatus = &autoStatus
	}
	w := do(t, h, "POST", "/api/boards/"+boardID+"/columns", req)
	if w.Code != 201 {
		t.Fatalf("create column: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c model.Column
	decode(t, w, &c)
	return c.ID
}

// ============================ Health ============================

func TestHealthEndpoint(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()

	w := do(t, h, "GET", "/health", nil)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]any
	decode(t, w, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("expected status ok, got %v", resp["status"])
	}
}

// ============================ Boards ============================

func TestBoardCRUD(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()

	// Create
	desc := "my board"
	w := do(t, h, "POST", "/api/boards", model.CreateBoardRequest{Name: "Board A", Description: &desc})
	if w.Code != 201 {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var b model.Board
	decode(t, w, &b)
	if b.Name != "Board A" || b.Description != "my board" {
		t.Fatalf("unexpected board: %+v", b)
	}

	// Get
	w = do(t, h, "GET", "/api/boards/"+b.ID, nil)
	if w.Code != 200 {
		t.Fatalf("get: expected 200, got %d", w.Code)
	}

	// List
	w = do(t, h, "GET", "/api/boards", nil)
	if w.Code != 200 {
		t.Fatalf("list: expected 200, got %d", w.Code)
	}
	var boards []model.Board
	decode(t, w, &boards)
	if len(boards) != 1 {
		t.Fatalf("expected 1 board, got %d", len(boards))
	}

	// Patch (rename + archive)
	newName := "Board A2"
	archived := true
	w = do(t, h, "PATCH", "/api/boards/"+b.ID, model.UpdateBoardRequest{Name: &newName, Archived: &archived})
	if w.Code != 200 {
		t.Fatalf("patch: expected 200, got %d", w.Code)
	}
	var updated model.Board
	decode(t, w, &updated)
	if updated.Name != "Board A2" || !updated.Archived {
		t.Fatalf("patch did not apply: %+v", updated)
	}

	// Archived board is excluded from the default list but included with include_archived.
	w = do(t, h, "GET", "/api/boards", nil)
	decode(t, w, &boards)
	if len(boards) != 0 {
		t.Fatalf("archived board should be hidden, got %d", len(boards))
	}
	w = do(t, h, "GET", "/api/boards?include_archived=true", nil)
	decode(t, w, &boards)
	if len(boards) != 1 {
		t.Fatalf("include_archived should show board, got %d", len(boards))
	}

	// Delete
	w = do(t, h, "DELETE", "/api/boards/"+b.ID, nil)
	if w.Code != 200 {
		t.Fatalf("delete: expected 200, got %d", w.Code)
	}
	w = do(t, h, "GET", "/api/boards/"+b.ID, nil)
	if w.Code != 404 {
		t.Fatalf("get after delete: expected 404, got %d", w.Code)
	}
}

func TestBoardErrors(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()

	// Missing name → 400
	if w := do(t, h, "POST", "/api/boards", model.CreateBoardRequest{}); w.Code != 400 {
		t.Fatalf("empty name: expected 400, got %d", w.Code)
	}
	// Invalid JSON → 400
	req := httptest.NewRequest("POST", "/api/boards", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("bad json: expected 400, got %d", w.Code)
	}
	// Unknown board → 404
	if w := do(t, h, "GET", "/api/boards/nope", nil); w.Code != 404 {
		t.Fatalf("unknown board: expected 404, got %d", w.Code)
	}
	// Unsupported method → 405
	if w := do(t, h, "DELETE", "/api/boards", nil); w.Code != 405 {
		t.Fatalf("delete collection: expected 405, got %d", w.Code)
	}
}

// ============================ Columns ============================

func TestColumnCRUD(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")

	// Create
	w := do(t, h, "POST", "/api/boards/"+boardID+"/columns", model.CreateColumnRequest{Name: "Todo"})
	if w.Code != 201 {
		t.Fatalf("create column: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var c model.Column
	decode(t, w, &c)
	if c.Name != "Todo" || c.BoardID != boardID {
		t.Fatalf("unexpected column: %+v", c)
	}

	// List
	w = do(t, h, "GET", "/api/boards/"+boardID+"/columns", nil)
	var cols []model.Column
	decode(t, w, &cols)
	if len(cols) != 1 {
		t.Fatalf("expected 1 column, got %d", len(cols))
	}

	// Get
	if w := do(t, h, "GET", "/api/columns/"+c.ID, nil); w.Code != 200 {
		t.Fatalf("get column: expected 200, got %d", w.Code)
	}

	// Patch
	name := "In Progress"
	auto := "open"
	w = do(t, h, "PATCH", "/api/columns/"+c.ID, model.UpdateColumnRequest{Name: &name, AutoStatus: &auto})
	if w.Code != 200 {
		t.Fatalf("patch column: expected 200, got %d", w.Code)
	}
	decode(t, w, &c)
	if c.Name != "In Progress" || c.AutoStatus == nil || *c.AutoStatus != "open" {
		t.Fatalf("patch did not apply: %+v", c)
	}

	// Delete
	if w := do(t, h, "DELETE", "/api/columns/"+c.ID, nil); w.Code != 200 {
		t.Fatalf("delete column: expected 200, got %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/columns/"+c.ID, nil); w.Code != 404 {
		t.Fatalf("get after delete: expected 404, got %d", w.Code)
	}
}

func TestColumnErrors(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")

	// Missing name → 400
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/columns", model.CreateColumnRequest{}); w.Code != 400 {
		t.Fatalf("empty name: expected 400, got %d", w.Code)
	}
	// Invalid auto_status at create → 400
	bad := "nonsense"
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/columns", model.CreateColumnRequest{Name: "X", AutoStatus: &bad}); w.Code != 400 {
		t.Fatalf("bad auto_status create: expected 400, got %d", w.Code)
	}
	// Invalid auto_status at patch → 400
	colID := mkColumn(t, h, boardID, "C", "")
	if w := do(t, h, "PATCH", "/api/columns/"+colID, model.UpdateColumnRequest{AutoStatus: &bad}); w.Code != 400 {
		t.Fatalf("bad auto_status patch: expected 400, got %d", w.Code)
	}
	// Column on a missing board → 404 (board lookup fails in store).
	if w := do(t, h, "POST", "/api/boards/missing/columns", model.CreateColumnRequest{Name: "X"}); w.Code != 404 {
		t.Fatalf("column on missing board: expected 404, got %d", w.Code)
	}
}

func TestReorderColumns(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	c1 := mkColumn(t, h, boardID, "A", "")
	c2 := mkColumn(t, h, boardID, "B", "")
	c3 := mkColumn(t, h, boardID, "C", "")

	// Reverse the order via explicit positions.
	body := map[string]any{"columns": []map[string]any{
		{"id": c1, "position": 3.0},
		{"id": c2, "position": 2.0},
		{"id": c3, "position": 1.0},
	}}
	w := do(t, h, "POST", "/api/boards/"+boardID+"/columns/reorder", body)
	if w.Code != 200 {
		t.Fatalf("reorder: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cols []model.Column
	decode(t, w, &cols)
	if len(cols) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(cols))
	}
	// ListColumns orders by position ASC → C, B, A.
	if cols[0].ID != c3 || cols[1].ID != c2 || cols[2].ID != c1 {
		t.Fatalf("unexpected order: %s, %s, %s", cols[0].Name, cols[1].Name, cols[2].Name)
	}
}

// ============================ Cards ============================

func TestCardCreateAndDelete(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")

	// Create a card → creates a noteboard item, then attaches it.
	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{
		Title: "First card", ColumnID: colID, Tags: []string{"x"},
	})
	if w.Code != 201 {
		t.Fatalf("create card: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cv model.CardView
	decode(t, w, &cv)
	if cv.Placement == nil || cv.Placement.ColumnID != colID {
		t.Fatalf("placement not set: %+v", cv.Placement)
	}
	cardID := cv.Placement.CardID
	if nb.status(cardID) != "open" {
		t.Fatalf("expected noteboard status open, got %q", nb.status(cardID))
	}

	// Board view should now contain the card in its column.
	w = do(t, h, "GET", "/api/boards/"+boardID+"/cards", nil)
	if w.Code != 200 {
		t.Fatalf("board view: expected 200, got %d", w.Code)
	}
	var view model.BoardView
	decode(t, w, &view)
	if len(view.Columns) != 1 || len(view.Columns[0].Cards) != 1 {
		t.Fatalf("expected 1 card in 1 column, got %+v", view.Columns)
	}

	// Soft delete (archive) the card → noteboard item flips to archived.
	if w := do(t, h, "DELETE", "/api/cards/"+cardID, nil); w.Code != 200 {
		t.Fatalf("delete card: expected 200, got %d", w.Code)
	}
	if nb.status(cardID) != "archived" {
		t.Fatalf("expected archived after soft delete, got %q", nb.status(cardID))
	}
}

// TestCardCreateAutoStatus verifies creating a card directly into a column with
// auto_status applies that status to the noteboard item (symmetry with move).
func TestCardCreateAutoStatus(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	doneCol := mkColumn(t, h, boardID, "Done", "done")

	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{
		Title: "Born done", ColumnID: doneCol,
	})
	if w.Code != 201 {
		t.Fatalf("create card: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cv model.CardView
	decode(t, w, &cv)
	if got := nb.status(cv.Placement.CardID); got != "done" {
		t.Fatalf("expected auto_status done on create, got %q", got)
	}
}

// TestCardMoveAutoStatus covers moving a card across columns and the auto_status
// PATCH to noteboard that a move into an auto_status column triggers.
func TestCardMoveAutoStatus(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	todo := mkColumn(t, h, boardID, "Todo", "")
	done := mkColumn(t, h, boardID, "Done", "done")

	// Create a card in Todo.
	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "Move me", ColumnID: todo})
	var cv model.CardView
	decode(t, w, &cv)
	cardID := cv.Placement.CardID
	if nb.status(cardID) != "open" {
		t.Fatalf("precondition: expected open, got %q", nb.status(cardID))
	}

	// Move it into Done.
	w = do(t, h, "POST", "/api/cards/"+cardID+"/move", model.MoveCardRequest{
		BoardID: boardID, ColumnID: done, Position: 1.0,
	})
	if w.Code != 200 {
		t.Fatalf("move: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	decode(t, w, &resp)
	if resp["auto_status_applied"] != "done" {
		t.Fatalf("expected auto_status_applied=done, got %v", resp["auto_status_applied"])
	}
	if nb.status(cardID) != "done" {
		t.Fatalf("expected noteboard status done after move, got %q", nb.status(cardID))
	}

	// Placements lookup should reflect the new column.
	w = do(t, h, "GET", "/api/cards/"+cardID+"/placements", nil)
	var ps []model.Placement
	decode(t, w, &ps)
	if len(ps) != 1 || ps[0].ColumnID != done {
		t.Fatalf("placement not moved: %+v", ps)
	}
}

func TestAttachExistingCard(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")

	// A noteboard item that already exists, independent of kanban.
	existing := nb.seedItem("Pre-existing item")
	w := do(t, h, "PUT", "/api/boards/"+boardID+"/cards/"+existing, model.AttachCardRequest{ColumnID: colID})
	if w.Code != 201 {
		t.Fatalf("attach: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Attaching an item noteboard doesn't know about → 404.
	w = do(t, h, "PUT", "/api/boards/"+boardID+"/cards/ghost", model.AttachCardRequest{ColumnID: colID})
	if w.Code != 404 {
		t.Fatalf("attach unknown item: expected 404, got %d", w.Code)
	}

	// Detach.
	if w := do(t, h, "DELETE", "/api/boards/"+boardID+"/cards/"+existing, nil); w.Code != 200 {
		t.Fatalf("detach: expected 200, got %d", w.Code)
	}
}

func TestWIPLimit(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")

	// Column with wip_limit 1.
	limit := 1
	w := do(t, h, "POST", "/api/boards/"+boardID+"/columns", model.CreateColumnRequest{Name: "Tight", WIPLimit: &limit})
	var c model.Column
	decode(t, w, &c)

	// First card fits.
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "one", ColumnID: c.ID}); w.Code != 201 {
		t.Fatalf("first card: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	// Second card exceeds the WIP limit → 409.
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "two", ColumnID: c.ID}); w.Code != 409 {
		t.Fatalf("second card: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCardErrors(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")

	// Missing title → 400.
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{ColumnID: colID}); w.Code != 400 {
		t.Fatalf("missing title: expected 400, got %d", w.Code)
	}
	// Missing column_id → 400.
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "x"}); w.Code != 400 {
		t.Fatalf("missing column_id: expected 400, got %d", w.Code)
	}
	// Column belonging to another board → 400.
	other := mkBoard(t, h, "Other")
	otherCol := mkColumn(t, h, other, "Elsewhere", "")
	if w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "x", ColumnID: otherCol}); w.Code != 400 {
		t.Fatalf("cross-board column: expected 400, got %d", w.Code)
	}
	// Move with missing board_id → 400.
	if w := do(t, h, "POST", "/api/cards/whatever/move", model.MoveCardRequest{ColumnID: colID, Position: 1}); w.Code != 400 {
		t.Fatalf("move missing board_id: expected 400, got %d", w.Code)
	}
	// Move a card with no placement → 404.
	if w := do(t, h, "POST", "/api/cards/ghost/move", model.MoveCardRequest{BoardID: boardID, ColumnID: colID, Position: 1}); w.Code != 404 {
		t.Fatalf("move nonexistent placement: expected 404, got %d", w.Code)
	}
}

// ============================ Card links ============================

func TestCardLinks(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")
	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "linked", ColumnID: colID})
	var cv model.CardView
	decode(t, w, &cv)
	cardID := cv.Placement.CardID

	// Create a link.
	label := "primary session"
	w = do(t, h, "POST", "/api/cards/"+cardID+"/links", model.CreateCardLinkRequest{
		EntityType: "session", EntityRef: "sess-123", Label: &label,
	})
	if w.Code != 201 {
		t.Fatalf("create link: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var link model.CardLink
	decode(t, w, &link)
	if link.EntityType != "session" || link.EntityRef != "sess-123" {
		t.Fatalf("unexpected link: %+v", link)
	}

	// List links.
	w = do(t, h, "GET", "/api/cards/"+cardID+"/links", nil)
	var links []model.CardLink
	decode(t, w, &links)
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}

	// Reverse lookup: cards by entity.
	w = do(t, h, "GET", "/api/entities/session/sess-123/cards", nil)
	if w.Code != 200 {
		t.Fatalf("entity cards: expected 200, got %d", w.Code)
	}
	var ec []map[string]any
	decode(t, w, &ec)
	if len(ec) != 1 || ec[0]["card_id"] != cardID {
		t.Fatalf("expected the linked card, got %+v", ec)
	}

	// Missing entity_type → 400.
	if w := do(t, h, "POST", "/api/cards/"+cardID+"/links", model.CreateCardLinkRequest{EntityRef: "x"}); w.Code != 400 {
		t.Fatalf("missing entity_type: expected 400, got %d", w.Code)
	}

	// Delete the link.
	if w := do(t, h, "DELETE", "/api/links/"+link.ID, nil); w.Code != 200 {
		t.Fatalf("delete link: expected 200, got %d", w.Code)
	}
	if w := do(t, h, "DELETE", "/api/links/"+link.ID, nil); w.Code != 404 {
		t.Fatalf("delete missing link: expected 404, got %d", w.Code)
	}
}

// One entity can be linked to several cards — an agent session picks up a
// dispatch card and then has its work classified onto more. A caller that has
// to name a single card ("which todo is this session for?") reads the first
// one, so the route must hand back the store's order untouched.
//
// The store-level guarantee (oldest link first, whatever order the rows were
// written in) is pinned in internal/db; this checks the route does not
// re-sort on the way out.
func TestEntityCardsAreOldestLinkFirst(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")

	const sessionRef = "sess-ordered"
	var wantOrder []string
	for _, title := range []string{"dispatch", "classified once", "classified twice"} {
		w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: title, ColumnID: colID})
		var cv model.CardView
		decode(t, w, &cv)
		cardID := cv.Placement.CardID
		wantOrder = append(wantOrder, cardID)
		if w := do(t, h, "POST", "/api/cards/"+cardID+"/links", model.CreateCardLinkRequest{
			EntityType: "session", EntityRef: sessionRef,
		}); w.Code != 201 {
			t.Fatalf("link %q: expected 201, got %d: %s", title, w.Code, w.Body.String())
		}
	}

	w := do(t, h, "GET", "/api/entities/session/"+sessionRef+"/cards", nil)
	if w.Code != 200 {
		t.Fatalf("entity cards: expected 200, got %d", w.Code)
	}
	var got []map[string]any
	decode(t, w, &got)
	if len(got) != len(wantOrder) {
		t.Fatalf("expected %d cards, got %d: %+v", len(wantOrder), len(got), got)
	}
	for i, want := range wantOrder {
		if got[i]["card_id"] != want {
			t.Errorf("card %d = %v, want %v (order must be oldest link first)", i, got[i]["card_id"], want)
		}
	}
}

// ============================ Search ============================

func TestSearch(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()
	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")
	do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{Title: "Findable card", ColumnID: colID})

	// Plain search delegates to noteboard.
	w := do(t, h, "GET", "/api/search?q=Findable", nil)
	if w.Code != 200 {
		t.Fatalf("search: expected 200, got %d", w.Code)
	}
	var items []map[string]any
	decode(t, w, &items)
	if len(items) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(items))
	}

	// on_board=true keeps only items with a placement (the created card qualifies).
	w = do(t, h, "GET", "/api/search?q=Findable&on_board=true", nil)
	decode(t, w, &items)
	if len(items) != 1 {
		t.Fatalf("on_board search: expected 1, got %d", len(items))
	}

	// Missing q → 400.
	if w := do(t, h, "GET", "/api/search", nil); w.Code != 400 {
		t.Fatalf("missing q: expected 400, got %d", w.Code)
	}
}
