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

	"github.com/kayushkin/kanban-store/internal/api"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/noteboard"
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
	// patchStatus, when non-zero, makes every PATCH answer that status instead
	// of applying the change. noteboard being reachable for the create and
	// unreachable a millisecond later is the ordinary case, not an exotic one.
	patchStatus int
}

func newFakeNoteboard() *fakeNoteboard {
	return &fakeNoteboard{items: map[string]map[string]any{}}
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
		includeDeleted := r.URL.Query().Get("include_deleted") == "true"
		f.mu.Lock()
		it, ok := f.items[id]
		if ok && !includeDeleted {
			// A deleted item is not found unless asked for by name.
			if _, deleted := it["deleted_at"]; deleted {
				ok = false
			}
		}
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
		if st := f.patchStatus; st != 0 {
			f.mu.Unlock()
			writeJSON(w, st, map[string]string{"error": "upstream refused the patch"})
			return
		}
		it, ok := f.items[id]
		var out map[string]any
		if ok {
			// noteboard decodes a PATCH into model.UpdateItemRequest NON-strictly:
			// a key that struct does not carry is dropped in silence and still
			// answered 200. Applying every key, as this fake used to, makes a
			// misspelled field look like it worked.
			for k, v := range patch {
				if noteboardUpdateFields[k] {
					it[k] = v
				}
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

	// A reversible delete stamps deleted_at and leaves status ALONE, exactly as
	// noteboard does (internal/db/db.go DeleteItem, verified against a real
	// noteboard binary on 2026-08-10). The row stops being visible: a later GET
	// answers 404. This fake previously flipped status to "archived", which no
	// version of noteboard has ever done, and a green test asserted it.
	mux.HandleFunc("DELETE /api/items/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		hard := r.URL.Query().Get("hard") == "true"
		f.mu.Lock()
		it, ok := f.items[id]
		if ok {
			if hard {
				delete(f.items, id)
			} else {
				it["deleted_at"] = "2026-08-10T00:00:00Z"
			}
		}
		f.mu.Unlock()
		if !ok {
			// noteboard answers a delete of an id it cannot find with 500 and the
			// raw driver message, not 404 — every other missing-item route on that
			// service answers 404. Reproduced rather than tidied, so this repo's
			// tests exercise the behaviour that actually reaches them.
			writeJSON(w, 500, map[string]string{"error": "sql: no rows in result set"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		if q == "" {
			writeJSON(w, 400, map[string]string{"error": "q parameter is required"})
			return
		}
		includeHeld := r.URL.Query().Get("include_held") == "true"
		f.mu.Lock()
		var out []map[string]any
		for _, it := range f.items {
			title, _ := it["title"].(string)
			if !strings.Contains(strings.ToLower(title), q) {
				continue
			}
			if _, deleted := it["deleted_at"]; deleted {
				continue
			}
			// Held items are withheld unless the caller asks for them — the board
			// asks, an agent's discovery query must not.
			if _, held := it["held_at"]; held && !includeHeld {
				continue
			}
			out = append(out, clone(it))
		}
		f.mu.Unlock()
		writeJSON(w, 200, out)
	})

	mux.HandleFunc("POST /api/items/{id}/hold", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.itemAction(w, r.PathValue("id"), func(it map[string]any) {
			it["held_at"] = "2026-08-10T00:00:00Z"
			it["hold_reason"] = req.Reason
		})
	})

	mux.HandleFunc("POST /api/items/{id}/unhold", func(w http.ResponseWriter, r *http.Request) {
		f.itemAction(w, r.PathValue("id"), func(it map[string]any) {
			delete(it, "held_at")
			delete(it, "hold_reason")
		})
	})

	return mux
}

// noteboardUpdateFields is the json tag set of noteboard's UpdateItemRequest,
// read from ~/repos/noteboard/model/model.go on 2026-08-10. A PATCH key outside
// this set is discarded by the real service without saying so.
var noteboardUpdateFields = map[string]bool{
	"title": true, "body": true, "tags": true, "priority": true, "rank": true,
	"status": true, "list_id": true, "due_at": true, "parent_id": true,
	"links": true, "schedule": true, "auto_hold_at_usd": true,
}

// itemAction applies mutate to a live item and answers the way noteboard's
// item sub-resources do: the updated item, or 404.
func (f *fakeNoteboard) itemAction(w http.ResponseWriter, id string, mutate func(map[string]any)) {
	f.mu.Lock()
	it, ok := f.items[id]
	var out map[string]any
	if ok {
		mutate(it)
		out = clone(it)
	}
	f.mu.Unlock()
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, 200, out)
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

// setup builds an API backed by a fresh temp SQLite store and an in-memory
// fake noteboard. The returned cleanup tears both down.
func setup(t *testing.T) (http.Handler, *fakeNoteboard, func()) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	nb := newFakeNoteboard()
	srv := httptest.NewServer(nb.handler())
	a := api.New(store, noteboard.New(srv.URL))
	return a.Handler(), nb, func() {
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

	// A reversible delete of the card. This assertion used to read "→ noteboard
	// item flips to archived", and it passed, because the fake it ran against
	// had been written from this repo's comments instead of from noteboard's
	// routes. noteboard stamps deleted_at and never touches status.
	if w := do(t, h, "DELETE", "/api/cards/"+cardID, nil); w.Code != 200 {
		t.Fatalf("delete card: expected 200, got %d", w.Code)
	}
	if got := nb.status(cardID); got != "open" {
		t.Fatalf("a reversible delete must leave status alone, got %q — deleting is the "+
			"item being taken away, archiving is a state the user chose for a live "+
			"item, and a restore has to be able to tell them apart", got)
	}

	// And this is what a soft delete actually does to the board: the placement
	// survives, the item read 404s, so the card leaves its column and lands in
	// the orphan bucket. Nothing hides it as an archived card, because nothing
	// ever marks it archived.
	w = do(t, h, "GET", "/api/boards/"+boardID+"/cards", nil)
	if w.Code != 200 {
		t.Fatalf("board view: expected 200, got %d", w.Code)
	}
	view = model.BoardView{}
	decode(t, w, &view)
	if len(view.Columns) != 1 || len(view.Columns[0].Cards) != 0 {
		t.Fatalf("deleted card must leave its column, got %+v", view.Columns)
	}
	if len(view.Orphans) != 1 || view.Orphans[0].Placement.CardID != cardID {
		t.Fatalf("deleted card must surface as an orphan placement, got %+v", view.Orphans)
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

// A card created straight into a column with auto_status carries TWO writes: the
// noteboard create, then a PATCH that makes the item's status match the column.
// The second one can fail on its own, and when it does the card exists in
// noteboard reading "open" while sitting in a Done column — which is precisely
// the state the auto_status write exists to prevent.
//
// moveCard already reports that failure as auto_status_error. createCard used to
// drop it: the `if perr == nil` guard had no else, so the response was a plain
// 201 carrying the pre-patch item. status "open" is also exactly what a column
// with no auto_status returns, so the failure produced a value indistinguishable
// from a legitimate answer, and noteboard is the source of truth that agents
// read to decide what work is still open.
func TestCardCreateReportsAFailedAutoStatus(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()

	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Done", "done")

	nb.patchStatus = 503

	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{
		Title: "lands in Done", ColumnID: colID,
	})
	if w.Code != 201 {
		t.Fatalf("create card: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cv model.CardView
	decode(t, w, &cv)

	if cv.AutoStatusError == "" {
		t.Fatalf("a failed auto_status write must be reported: the card is in a column "+
			"whose auto_status is %q and its noteboard item still reads %q, and the "+
			"response says nothing — got %s", "done", nb.status(cv.Placement.CardID),
			w.Body.String())
	}
	if cv.AutoStatusApplied != "" {
		t.Errorf("auto_status was not applied, so it must not be reported as applied: %q",
			cv.AutoStatusApplied)
	}
}

// The other side of the same contract: when the write succeeds, say so, and say
// it with the same field moveCard uses.
func TestCardCreateReportsAnAppliedAutoStatus(t *testing.T) {
	h, nb, cleanup := setup(t)
	defer cleanup()

	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Done", "done")

	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{
		Title: "lands in Done", ColumnID: colID,
	})
	if w.Code != 201 {
		t.Fatalf("create card: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var cv model.CardView
	decode(t, w, &cv)

	if cv.AutoStatusApplied != "done" {
		t.Errorf("auto_status_applied: got %q, want done", cv.AutoStatusApplied)
	}
	if cv.AutoStatusError != "" {
		t.Errorf("auto_status succeeded, so no error must be reported: %q", cv.AutoStatusError)
	}
	if got := nb.status(cv.Placement.CardID); got != "done" {
		t.Errorf("noteboard status: got %q, want done", got)
	}
}

// A column with no auto_status must stay silent on both fields — otherwise the
// "applied" field cannot be read as evidence that anything happened.
func TestCardCreateWithoutAutoStatusReportsNeither(t *testing.T) {
	h, _, cleanup := setup(t)
	defer cleanup()

	boardID := mkBoard(t, h, "Board")
	colID := mkColumn(t, h, boardID, "Todo", "")

	w := do(t, h, "POST", "/api/boards/"+boardID+"/cards", model.CreateCardRequest{
		Title: "plain", ColumnID: colID,
	})
	var cv model.CardView
	decode(t, w, &cv)
	if cv.AutoStatusApplied != "" || cv.AutoStatusError != "" {
		t.Errorf("no auto_status column must report neither field, got applied=%q error=%q",
			cv.AutoStatusApplied, cv.AutoStatusError)
	}
}
