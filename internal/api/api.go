package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kayushkin/kanban-store/internal/bundlestore"
	"github.com/kayushkin/kanban-store/internal/config"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/llmbridge"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/noteboard"
	"github.com/kayushkin/kanban-store/internal/principalstore"
	"github.com/kayushkin/kanban-store/internal/timeaccounting"
)

type API struct {
	store     *db.Store
	noteboard *noteboard.Client
	// principals is consulted on exactly one write: assigning a principal to a
	// card. See internal/principalstore for why this otherwise-dumb store checks
	// that one reference.
	principals *principalstore.Client
	// bridge is consulted on one write: setting a board's default agent or
	// default instance. See internal/llmbridge for why.
	bridge *llmbridge.Client
	// bundles is consulted on one write: setting a board's default bundle.
	bundles *bundlestore.Client
}

func New(store *db.Store, nb *noteboard.Client, principals *principalstore.Client, bridge *llmbridge.Client, bundles *bundlestore.Client) *API {
	return &API{store: store, noteboard: nb, principals: principals, bridge: bridge, bundles: bundles}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", a.health)

	// boards
	mux.HandleFunc("/api/boards", a.boards)
	mux.HandleFunc("/api/boards/", a.boardsTree)

	// columns (single-resource ops, and a column's cards a page at a time)
	mux.HandleFunc("/api/columns/", a.columnScoped)

	// cards (single-resource ops, including move + delete)
	mux.HandleFunc("/api/cards/", a.cardScoped)

	// links (delete by link id)
	mux.HandleFunc("/api/links/", a.linkByID)

	// card notes (delete by note id)
	mux.HandleFunc("/api/notes/", a.notesByID)

	// reverse lookups by entity
	mux.HandleFunc("/api/entities/", a.entityScoped)

	// reverse lookup by principal: every card someone is assigned to
	mux.HandleFunc("/api/assignments", a.assignmentsByPrincipal)

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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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
	case "priority-levels":
		if len(parts) == 2 {
			a.boardPriorityLevels(w, r, boardID)
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
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := a.checkBoardSettings(&req); err != nil {
			writeSettingsCheckFailure(w, err)
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

// columnScoped routes /api/columns/:id and /api/columns/:id/cards.
func (a *API) columnScoped(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/columns/")
	if rest == "" {
		writeError(w, 400, "missing column id")
		return
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 2 && parts[1] == "cards" {
		a.columnCards(w, r, parts[0])
		return
	}
	if len(parts) != 1 {
		writeError(w, 404, "not found")
		return
	}
	a.columnByID(w, r, parts[0])
}

func (a *API) columnByID(w http.ResponseWriter, r *http.Request, id string) {
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
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
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
		// A board with 6,466 cards on it answered 12 MB per read, on a page that
		// polls every fifteen seconds. limit caps each column; the column's own
		// endpoint fetches the rest a page at a time.
		limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
		if err != nil {
			limit = 0
		}
		view, err := a.assembleBoardView(boardID, limit)
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
		defaultAssignee, err := a.boardDefaultAssigneeFor(boardID)
		if err != nil {
			writeSettingsCheckFailure(w, err)
			return
		}
		p, err := a.store.AttachCard(boardID, cardID, &req)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		if err := a.recordEvent(&model.CardEvent{
			CardID: cardID, BoardID: boardID, Kind: model.EventCardAttached,
			ClockState: a.clockStateForColumn(cardID, boardID, req.ColumnID),
			Actor:      actorFrom(r), ToColumnID: req.ColumnID,
		}); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if _, err := a.applyBoardDefaultAssignee(cardID, defaultAssignee, actorFrom(r)); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, p)
	case "DELETE":
		if err := a.store.DetachCard(boardID, cardID); err != nil {
			mapDBErr(w, err)
			return
		}
		// The card left this board, so its clock on this board stops. The log stays:
		// a card that was here and went is part of what happened.
		if err := a.recordEvent(&model.CardEvent{
			CardID: cardID, BoardID: boardID, Kind: model.EventCardDetached,
			ClockState: model.ClockStopped, Actor: actorFrom(r),
		}); err != nil {
			writeError(w, 500, err.Error())
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
	defaultAssignee, err := a.boardDefaultAssigneeFor(boardID)
	if err != nil {
		writeSettingsCheckFailure(w, err)
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
	// The card's clock starts here. A card created straight into a column that
	// stops the clock (a classifier filing already-handled mail under "No action")
	// is born finished, and says so.
	if err := a.recordEvent(&model.CardEvent{
		CardID: cardID, BoardID: boardID, Kind: model.EventCardCreated,
		ClockState: a.clockStateForColumn(cardID, boardID, req.ColumnID),
		Actor:      actorFrom(r), Summary: req.Title, ToColumnID: req.ColumnID,
	}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	assignments, err := a.applyBoardDefaultAssignee(cardID, defaultAssignee, actorFrom(r))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, model.CardView{Placement: p, Item: item, Assignments: assignments})
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
//	/api/cards/:cardID/events         — GET, POST  (the action log)
//	/api/cards/:cardID/notes          — GET, POST  (status updates and summaries)
//	/api/cards/:cardID/timeline       — GET   (events + notes + the time they took)
//	/api/cards/:cardID/assignments    — GET   (who is on the card)
//	/api/cards/:cardID/assignments/:principalID — PUT (idempotent) | DELETE
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
	case "events":
		a.cardEvents(w, r, cardID)
		return
	case "notes":
		a.cardNotes(w, r, cardID)
		return
	case "timeline":
		a.cardTimeline(w, r, cardID)
		return
	case "assignments":
		if len(parts) == 2 {
			a.cardAssignments(w, r, cardID)
			return
		}
		if len(parts) == 3 && parts[2] != "" {
			a.cardAssignmentByPrincipal(w, r, cardID, parts[2])
			return
		}
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
	holdReason := ""
	if hold {
		var req struct {
			Reason string `json:"reason"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		holdReason = req.Reason
		item, err = a.noteboard.HoldItem(cardID, req.Reason)
	} else {
		item, err = a.noteboard.UnholdItem(cardID)
	}
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	// Pressing stop parks the work wherever it sits, so the clock pauses on every
	// board at once — which is why this event carries no board.
	kind, state := model.EventCardUnheld, model.ClockRunning
	if hold {
		kind, state = model.EventCardHeld, model.ClockPaused
	}
	if err := a.recordEvent(&model.CardEvent{
		CardID: cardID, Kind: kind, ClockState: state, Actor: actorFrom(r), Summary: holdReason,
	}); err != nil {
		writeError(w, 500, err.Error())
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
		// Completing a card stops its clock, however the completion was expressed —
		// dragged into a Done column, or patched straight to done from anywhere.
		if status, ok := patch["status"].(string); ok && (status == "done" || status == "archived") {
			if err := a.recordEvent(&model.CardEvent{
				CardID: cardID, Kind: model.EventCardCompleted, ClockState: model.ClockStopped,
				Actor: actorFrom(r), Summary: status,
			}); err != nil {
				writeError(w, 500, err.Error())
				return
			}
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
	fromColumn := ""
	if current != nil {
		fromColumn = current.ColumnID
	}
	if err := a.recordEvent(&model.CardEvent{
		CardID: cardID, BoardID: req.BoardID, Kind: model.EventCardMoved,
		ClockState:   a.clockStateForColumn(cardID, req.BoardID, req.ColumnID),
		Actor:        actorFrom(r),
		FromColumnID: fromColumn, ToColumnID: req.ColumnID,
	}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
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
		// Mail arriving and an agent being handed the work are things that happened
		// to the card, so they join its timeline. Every other kind of link is a fact
		// about the card rather than an event, and is not logged.
		kind, state, isAction := clockStateForEntityLink(req.EntityType)
		if req.ClockState != "" {
			if !model.ValidClockState(req.ClockState) {
				writeError(w, 400, "clock_state must be one of: "+strings.Join(model.ClockStateNames(), ", "))
				return
			}
			state = req.ClockState
		}
		if (req.OccurredAt != nil || req.ClockState != "") && !isAction {
			// Refused rather than ignored. A caller that backdates a repo link has
			// misunderstood what the link is, and answering 201 would let it believe
			// it had moved something on the timeline.
			writeError(w, 400, fmt.Sprintf(
				"occurred_at and clock_state are only meaningful for a link that records an action; %q records a fact about the card and is never logged",
				req.EntityType))
			return
		}
		l, err := a.store.CreateCardLink(cardID, &req)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if isAction {
			// The link's CreatedAt is when we recorded it; occurred_at is when it
			// happened. They differ for anything filed off a backlog.
			occurred := l.CreatedAt
			if req.OccurredAt != nil {
				occurred = *req.OccurredAt
			}
			if err := a.recordEvent(&model.CardEvent{
				CardID: cardID, Kind: kind, ClockState: state, Actor: actorFrom(r),
				Summary: l.Label, OccurredAt: occurred,
				Detail: json.RawMessage(fmt.Sprintf(`{"entity_type":%q,"entity_ref":%q}`, req.EntityType, req.EntityRef)),
			}); err != nil {
				writeError(w, 500, err.Error())
				return
			}
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

// assembleBoardView builds the board. limit caps how many cards each column
// carries, in stored order; 0 means all of them.
//
// ⚠️ Paging is in STORED order, which is not the order the board displays. What
// a client sorts by — priority, due date, title — lives in noteboard, so sorting
// the whole board server-side would mean fetching every item on it, which is the
// cost paging exists to avoid. A client showing a page therefore sorts what it
// has, and has to say so.
func (a *API) assembleBoardView(boardID string, limit int) (*model.BoardView, error) {
	b, err := a.store.GetBoard(boardID)
	if err != nil {
		return nil, err
	}
	cols, err := a.store.ListColumns(boardID)
	if err != nil {
		return nil, err
	}
	// Per column, so a limit means "this many of each" rather than a slice of one
	// arbitrary column, and so the count a client needs for "show more" is exact.
	var placements []*model.Placement
	totals := map[string]int{}
	for _, c := range cols {
		total, err := a.store.CountColumnCards(c.ID)
		if err != nil {
			return nil, err
		}
		totals[c.ID] = total
		page, err := a.store.ListPlacementsByColumn(c.ID, limit, 0)
		if err != nil {
			return nil, err
		}
		placements = append(placements, page...)
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
	// Events and links for exactly the cards on screen, one query each. The links
	// used to be one query per card, which on the largest board here is 6,466 of
	// them for a single read.
	eventsByCard, err := a.store.ListCardEventsForCards(ids, boardID)
	if err != nil {
		return nil, err
	}
	linksByCard, err := a.store.ListCardLinksForCards(ids)
	if err != nil {
		return nil, err
	}
	assignmentsByCard, err := a.store.ListCardAssignmentsForCards(ids)
	if err != nil {
		return nil, err
	}
	ladder, err := a.store.GetPriorityLadder(boardID)
	if err != nil {
		return nil, err
	}
	asOf := time.Now().UTC()

	// Bucket by column.
	byCol := map[string][]model.CardView{}
	var orphans []model.CardView
	for i, p := range placements {
		cv := model.CardView{
			Placement: p, Item: items[i], Links: linksByCard[p.CardID], Assignments: assignmentsByCard[p.CardID],
		}
		summary, _ := timeaccounting.Compute(timeaccounting.Input{
			Events: eventsByCard[p.CardID],
			Level:  ladder.LevelFor(priorityOfItem(items[i])),
			Hours:  b.BusinessHours,
			Now:    asOf,
		})
		// The segments are the timeline's working, and the timeline endpoint is
		// where they belong. A board asks how much time, not which stretches of it.
		summary.Segments = nil
		cv.Time = summary
		if items[i] == nil {
			orphans = append(orphans, cv)
			continue
		}
		byCol[p.ColumnID] = append(byCol[p.ColumnID], cv)
	}
	colViews := make([]model.ColumnView, len(cols))
	for i, c := range cols {
		colViews[i] = model.ColumnView{Column: c, Cards: byCol[c.ID], Total: totals[c.ID]}
	}
	return &model.BoardView{Board: b, Columns: colViews, Orphans: orphans, PriorityLadder: ladder}, nil
}
