package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kayushkin/kanban-store/internal/config"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/noteboard"
)

type API struct {
	store     *db.Store
	noteboard *noteboard.Client
}

func New(store *db.Store, nb *noteboard.Client) *API {
	return &API{store: store, noteboard: nb}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)

	// boards
	mux.HandleFunc("/api/boards", a.boards)
	mux.HandleFunc("/api/boards/", a.boardsTree)

	// columns (single-resource ops)
	mux.HandleFunc("/api/columns/", a.columnByID)

	// cards (single-resource ops, including move + delete)
	mux.HandleFunc("/api/cards/", a.cardScoped)

	// links (delete by link id)
	mux.HandleFunc("/api/links/", a.linkByID)

	// reverse lookups by entity
	mux.HandleFunc("/api/entities/", a.entityScoped)

	// entity-type registry & cross-entity tag listing
	mux.HandleFunc("/api/entity-types", a.entityTypes)
	mux.HandleFunc("/api/tags", a.allTags)

	// search delegate
	mux.HandleFunc("/api/search", a.search)

	return cors(mux)
}

func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func mapDBErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, 404, "not found")
		return
	}
	writeError(w, 500, err.Error())
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	counts, err := a.store.Counts()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "counts": counts})
}

// ============================ Boards ============================

func (a *API) boards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		includeArchived := r.URL.Query().Get("include_archived") == "true"
		boards, err := a.store.ListBoards(includeArchived)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, boards)
	case "POST":
		var req model.CreateBoardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		b, err := a.store.CreateBoard(&req)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, b)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// boardsTree handles /api/boards/:id and its sub-resources.
//
//	/api/boards/:id
//	/api/boards/:id/columns
//	/api/boards/:id/columns/reorder
//	/api/boards/:id/cards
//	/api/boards/:id/cards/:cardID   (PUT to attach existing card)
func (a *API) boardsTree(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/boards/")
	if rest == "" {
		writeError(w, 400, "missing board id")
		return
	}
	parts := strings.Split(rest, "/")
	boardID := parts[0]

	if len(parts) == 1 {
		a.boardByID(w, r, boardID)
		return
	}
	switch parts[1] {
	case "columns":
		if len(parts) == 2 {
			a.boardColumns(w, r, boardID)
			return
		}
		if len(parts) == 3 && parts[2] == "reorder" {
			a.reorderColumns(w, r, boardID)
			return
		}
	case "cards":
		if len(parts) == 2 {
			a.boardCards(w, r, boardID)
			return
		}
		if len(parts) == 3 {
			a.boardCardByID(w, r, boardID, parts[2])
			return
		}
	}
	writeError(w, 404, "not found")
}

func (a *API) boardByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case "GET":
		b, err := a.store.GetBoard(id)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, b)
	case "PATCH":
		var req model.UpdateBoardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		b, err := a.store.UpdateBoard(id, &req)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, b)
	case "DELETE":
		if err := a.store.DeleteBoard(id); err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ============================ Columns ============================

func (a *API) boardColumns(w http.ResponseWriter, r *http.Request, boardID string) {
	switch r.Method {
	case "GET":
		cols, err := a.store.ListColumns(boardID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, cols)
	case "POST":
		var req model.CreateColumnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		c, err := a.store.CreateColumn(boardID, &req)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 201, c)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// reorderColumns accepts {"columns":[{"id":"...","position":1.0}, ...]}.
func (a *API) reorderColumns(w http.ResponseWriter, r *http.Request, boardID string) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var req struct {
		Columns []struct {
			ID       string  `json:"id"`
			Position float64 `json:"position"`
		} `json:"columns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	for _, c := range req.Columns {
		if _, err := a.store.UpdateColumn(c.ID, &model.UpdateColumnRequest{Position: &c.Position}); err != nil {
			mapDBErr(w, err)
			return
		}
	}
	cols, err := a.store.ListColumns(boardID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cols)
}

func (a *API) columnByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/columns/")
	if id == "" {
		writeError(w, 400, "missing column id")
		return
	}
	switch r.Method {
	case "GET":
		c, err := a.store.GetColumn(id)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, c)
	case "PATCH":
		var req model.UpdateColumnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if req.AutoStatus != nil && *req.AutoStatus != "" && *req.AutoStatus != "open" && *req.AutoStatus != "done" && *req.AutoStatus != "archived" {
			writeError(w, 400, "auto_status must be one of: open, done, archived")
			return
		}
		c, err := a.store.UpdateColumn(id, &req)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, c)
	case "DELETE":
		if err := a.store.DeleteColumn(id); err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ============================ Cards (board-scoped) ============================

func (a *API) boardCards(w http.ResponseWriter, r *http.Request, boardID string) {
	switch r.Method {
	case "GET":
		view, err := a.assembleBoardView(boardID)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, view)
	case "POST":
		a.createCardOnBoard(w, r, boardID)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) boardCardByID(w http.ResponseWriter, r *http.Request, boardID, cardID string) {
	switch r.Method {
	case "PUT":
		// Attach an existing noteboard item to this board.
		var req model.AttachCardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		// Verify the card actually exists in noteboard.
		if _, err := a.noteboard.GetItem(cardID); err != nil {
			writeError(w, 404, "noteboard item not found: "+cardID)
			return
		}
		if err := a.checkWIP(req.ColumnID); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		p, err := a.store.AttachCard(boardID, cardID, &req)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 201, p)
	case "DELETE":
		if err := a.store.DetachCard(boardID, cardID); err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) createCardOnBoard(w http.ResponseWriter, r *http.Request, boardID string) {
	var req model.CreateCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	col, err := a.store.GetColumn(req.ColumnID)
	if err != nil {
		mapDBErr(w, err)
		return
	}
	if col.BoardID != boardID {
		writeError(w, 400, "column does not belong to this board")
		return
	}
	if err := a.checkWIP(req.ColumnID); err != nil {
		writeError(w, 409, err.Error())
		return
	}

	// Create the noteboard item first — it's the source of truth.
	item, err := a.noteboard.CreateItem(noteboard.CreateItemPayload{
		Type:          "todo",
		Title:         req.Title,
		Body:          req.Body,
		Tags:          req.Tags,
		Priority:      req.Priority,
		ListID:        req.ListID,
		DueAt:         req.DueAt,
		ParentID:      req.ParentID,
		Hold:          req.Hold,
		HoldReason:    req.HoldReason,
		AutoHoldAtUSD: req.AutoHoldAtUSD,
	})
	if err != nil {
		writeError(w, 502, "noteboard create failed: "+err.Error())
		return
	}
	cardID, _ := item["id"].(string)
	if cardID == "" {
		writeError(w, 502, "noteboard returned no id")
		return
	}
	// Then attach.
	p, err := a.store.AttachCard(boardID, cardID, &model.AttachCardRequest{
		ColumnID: req.ColumnID,
		Position: req.Position,
	})
	// If the destination column has auto_status, apply it now so card creation
	// is symmetric with MoveCard (otherwise classifier-created cards in Done
	// stay status=open in noteboard). Best-effort; failure is non-fatal.
	if col.AutoStatus != nil && *col.AutoStatus != "" {
		if patched, perr := a.noteboard.PatchItem(cardID, map[string]any{"status": *col.AutoStatus}); perr == nil {
			item = patched
		}
	}
	if err != nil {
		// Best-effort cleanup: the noteboard item now exists with no placement.
		// Leaving it in place is the safer default — user can find it on /notes.
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, model.CardView{Placement: p, Item: item})
}

// checkWIP returns an error if attaching another card would exceed the column's wip_limit.
func (a *API) checkWIP(columnID string) error {
	col, err := a.store.GetColumn(columnID)
	if err != nil {
		return err
	}
	if col.WIPLimit == nil {
		return nil
	}
	n, err := a.store.CountColumnCards(columnID)
	if err != nil {
		return err
	}
	if n >= *col.WIPLimit {
		return errors.New("WIP limit reached for column")
	}
	return nil
}

// ============================ Cards (single-resource) ============================
//
//	/api/cards/:cardID                — PATCH (forward to noteboard) | DELETE (archive)
//	/api/cards/:cardID/move           — POST  (move within/across columns)
//	/api/cards/:cardID/placements     — GET   (all boards this card is on)
//	/api/cards/:cardID/links          — GET, POST
func (a *API) cardScoped(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/cards/")
	if rest == "" {
		writeError(w, 400, "missing card id")
		return
	}
	parts := strings.Split(rest, "/")
	cardID := parts[0]

	if len(parts) == 1 {
		a.cardByID(w, r, cardID)
		return
	}
	switch parts[1] {
	case "move":
		a.moveCard(w, r, cardID)
		return
	case "placements":
		a.cardPlacements(w, r, cardID)
		return
	case "links":
		a.cardLinks(w, r, cardID)
		return
	case "hold":
		a.holdCard(w, r, cardID, true)
		return
	case "unhold":
		a.holdCard(w, r, cardID, false)
		return
	}
	writeError(w, 404, "not found")
}

// holdCard is the stop/play button. The hold lives on the noteboard item, not on
// this board — which is what lets a card be paused in ANY column instead of only
// by being dragged into a designated gate column, and what makes the pause bind
// on the noteboard-discovery path that never looks at a board at all.
func (a *API) holdCard(w http.ResponseWriter, r *http.Request, cardID string, hold bool) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var item noteboard.Item
	var err error
	if hold {
		var req struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		item, err = a.noteboard.HoldItem(cardID, req.Reason)
	} else {
		item, err = a.noteboard.UnholdItem(cardID)
	}
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, item)
}

func (a *API) cardByID(w http.ResponseWriter, r *http.Request, cardID string) {
	switch r.Method {
	case "PATCH":
		// Forward arbitrary patch to noteboard so callers can edit title/body/tags/etc.
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		item, err := a.noteboard.PatchItem(cardID, patch)
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		writeJSON(w, 200, item)
	case "DELETE":
		hard := r.URL.Query().Get("hard") == "true"
		if err := a.noteboard.DeleteItem(cardID, hard); err != nil {
			writeError(w, 502, err.Error())
			return
		}
		// On hard delete the item is gone for good, so drop its placements too.
		// On a reversible delete the placements stay, because the delete itself
		// is undoable and restoring the item has to restore it onto its boards.
		// Until then the card still occupies its column with a null item — the
		// same shape as an orphan — so a frontend must render that case.
		if hard {
			_ = a.store.DetachCardEverywhere(cardID)
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) moveCard(w http.ResponseWriter, r *http.Request, cardID string) {
	if r.Method != "POST" {
		writeError(w, 405, "method not allowed")
		return
	}
	var req model.MoveCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	// WIP check only when crossing into a new column.
	current, err := a.store.GetPlacement(cardID, req.BoardID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(w, 500, err.Error())
		return
	}
	if current == nil || current.ColumnID != req.ColumnID {
		if err := a.checkWIP(req.ColumnID); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	p, err := a.store.MoveCard(cardID, &req)
	if err != nil {
		mapDBErr(w, err)
		return
	}
	// Auto-status: if the destination column has auto_status set, PATCH the
	// noteboard item to match. Failure here is non-fatal (the move succeeded
	// in kanban-store) but logged in the response.
	col, _ := a.store.GetColumn(req.ColumnID)
	resp := map[string]any{"placement": p}
	if col != nil && col.AutoStatus != nil && *col.AutoStatus != "" {
		if _, err := a.noteboard.PatchItem(cardID, map[string]any{"status": *col.AutoStatus}); err != nil {
			resp["auto_status_error"] = err.Error()
		} else {
			resp["auto_status_applied"] = *col.AutoStatus
		}
	}
	writeJSON(w, 200, resp)
}

func (a *API) cardPlacements(w http.ResponseWriter, r *http.Request, cardID string) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	ps, err := a.store.ListPlacementsByCard(cardID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, ps)
}

// ============================ Card links ============================

func (a *API) cardLinks(w http.ResponseWriter, r *http.Request, cardID string) {
	switch r.Method {
	case "GET":
		ls, err := a.store.ListCardLinks(cardID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, ls)
	case "POST":
		var req model.CreateCardLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		l, err := a.store.CreateCardLink(cardID, &req)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, l)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) linkByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/links/")
	if id == "" {
		writeError(w, 400, "missing link id")
		return
	}
	if r.Method != "DELETE" {
		writeError(w, 405, "method not allowed")
		return
	}
	if err := a.store.DeleteCardLink(id); err != nil {
		mapDBErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ============================ Entities ============================
//
//	/api/entities/:type/:ref/cards
//	/api/entities/:type/:ref/tags
//	/api/entities/:type/:ref/tags/:tag
func (a *API) entityScoped(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/entities/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		writeError(w, 400, "expected /api/entities/:type/:ref/...")
		return
	}
	etype, eref, sub := parts[0], parts[1], parts[2]

	switch sub {
	case "cards":
		if r.Method != "GET" {
			writeError(w, 405, "method not allowed")
			return
		}
		ids, err := a.store.ListCardsByEntity(etype, eref)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		items, err := a.noteboard.GetItems(ids)
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		// Return parallel id/item arrays so callers can spot orphans (item==null).
		out := make([]map[string]any, len(ids))
		for i, id := range ids {
			out[i] = map[string]any{"card_id": id, "item": items[i]}
		}
		writeJSON(w, 200, out)
	case "tags":
		if len(parts) == 4 {
			a.entityTagOne(w, r, etype, eref, parts[3])
			return
		}
		a.entityTagsList(w, r, etype, eref)
	default:
		writeError(w, 404, "not found")
	}
}

func (a *API) entityTagsList(w http.ResponseWriter, r *http.Request, etype, eref string) {
	switch r.Method {
	case "GET":
		ts, err := a.store.ListEntityTags(etype, eref)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, ts)
	case "POST":
		var req model.CreateEntityTagRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		t, err := a.store.AddEntityTag(etype, eref, req.Tag)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, t)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) entityTagOne(w http.ResponseWriter, r *http.Request, etype, eref, tag string) {
	if r.Method != "DELETE" {
		writeError(w, 405, "method not allowed")
		return
	}
	if err := a.store.DeleteEntityTag(etype, eref, tag); err != nil {
		mapDBErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *API) entityTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, config.EntityTypes)
}

func (a *API) allTags(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tag := q.Get("tag")
	etype := q.Get("entity_type")
	if tag != "" {
		results, err := a.store.FindEntitiesByTag(tag, etype)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, results)
		return
	}
	tags, err := a.store.ListAllEntityTags()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, tags)
}

// ============================ Search ============================

// search delegates to noteboard FTS, then optionally filters to items present
// on a board (?board_id=) or in any kanban placement (?on_board=true).
func (a *API) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeError(w, 400, "q is required")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := a.noteboard.Search(query, limit)
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	boardID := q.Get("board_id")
	onBoard := q.Get("on_board") == "true"
	if boardID == "" && !onBoard {
		writeJSON(w, 200, items)
		return
	}

	// Filter: keep only items that have at least one placement (optionally on the named board).
	out := items[:0]
	for _, it := range items {
		id, _ := it["id"].(string)
		if id == "" {
			continue
		}
		ps, err := a.store.ListPlacementsByCard(id)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		match := false
		for _, p := range ps {
			if boardID != "" && p.BoardID == boardID {
				match = true
				break
			}
			if boardID == "" && onBoard {
				match = true
				break
			}
		}
		if match {
			out = append(out, it)
		}
	}
	writeJSON(w, 200, out)
}

// ============================ Board view assembly ============================

func (a *API) assembleBoardView(boardID string) (*model.BoardView, error) {
	b, err := a.store.GetBoard(boardID)
	if err != nil {
		return nil, err
	}
	cols, err := a.store.ListColumns(boardID)
	if err != nil {
		return nil, err
	}
	placements, err := a.store.ListPlacementsByBoard(boardID)
	if err != nil {
		return nil, err
	}
	// Fetch all noteboard items in one fan-out.
	ids := make([]string, len(placements))
	for i, p := range placements {
		ids[i] = p.CardID
	}
	items, err := a.noteboard.GetItems(ids)
	if err != nil {
		return nil, err
	}
	// Bucket by column.
	byCol := map[string][]model.CardView{}
	var orphans []model.CardView
	for i, p := range placements {
		links, _ := a.store.ListCardLinks(p.CardID)
		cv := model.CardView{Placement: p, Item: items[i], Links: links}
		if items[i] == nil {
			orphans = append(orphans, cv)
			continue
		}
		byCol[p.ColumnID] = append(byCol[p.ColumnID], cv)
	}
	colViews := make([]model.ColumnView, len(cols))
	for i, c := range cols {
		colViews[i] = model.ColumnView{Column: c, Cards: byCol[c.ID]}
	}
	return &model.BoardView{Board: b, Columns: colViews, Orphans: orphans}, nil
}
