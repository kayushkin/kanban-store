package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/timeaccounting"
)

// ============================ Recording ============================

// recordEvent appends an action to a card's log and reports the failure rather
// than hiding it: a move that succeeded but went unlogged leaves the card's
// figures quietly wrong from then on, and a caller that never hears about it
// cannot know the timeline it later reads is short an entry.
//
// The write paths call this after the change they describe has succeeded, so a
// rejected move never lands in the log as one that happened.
func (a *API) recordEvent(e *model.CardEvent) error {
	_, err := a.store.RecordCardEvent(e)
	if err != nil {
		return fmt.Errorf("record %s event for card %s: %w", e.Kind, e.CardID, err)
	}
	return nil
}

// clockStateForColumn says what landing in a column means for the budget clock.
//
// This is a state machine, not a chain of guesses. A column that has been
// classified decides. A column that has not says nothing about the clock, so the
// card keeps the state its last action left it in — moving an unclassified card
// between unclassified columns must not invent a change. With no history at all
// the card is starting its life on a board, and work that exists and is not
// declared blocked is work that can be done, so the clock runs.
func (a *API) clockStateForColumn(cardID, boardID, columnID string) model.ClockState {
	if col, err := a.store.GetColumn(columnID); err == nil && col.BudgetClockState != nil && *col.BudgetClockState != "" {
		return *col.BudgetClockState
	}
	events, err := a.store.ListCardEvents(cardID, boardID)
	if err == nil && len(events) > 0 {
		return events[len(events)-1].ClockState
	}
	return model.ClockRunning
}

// clockStateForEntityLink maps a link onto what it means for the clock, and
// reports ok=false for links that are not actions at all.
//
// Only two entity types are actions. 'email_msgid' is the same arrival recorded
// under its RFC identity and would double-count it; 'email_sender' is a learned
// affinity between a sender and a card rather than an event; a repo or a machine
// link is a fact about the card, not something that happened to it.
func clockStateForEntityLink(entityType string) (model.EventKind, model.ClockState, bool) {
	switch entityType {
	case "email":
		return model.EventEmailReceived, model.ClockRunning, true
	case "session":
		return model.EventAgentDispatched, model.ClockRunning, true
	}
	return "", "", false
}

// actorFrom reads who is acting from the request. Clients that do not say stay
// anonymous — an empty actor is an honest "we were not told", where a guess from
// the user agent would put a name on the timeline that nobody typed.
func actorFrom(r *http.Request) string {
	return r.URL.Query().Get("actor")
}

// ============================ Card events ============================

func (a *API) cardEvents(w http.ResponseWriter, r *http.Request, cardID string) {
	switch r.Method {
	case "GET":
		events, err := a.store.ListCardEvents(cardID, r.URL.Query().Get("board_id"))
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, events)
	case "POST":
		var req model.CreateCardEventRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		state, _ := req.ResolveClockState()
		actor := req.Actor
		if actor == "" {
			actor = actorFrom(r)
		}
		stored, err := a.store.RecordCardEvent(&model.CardEvent{
			CardID: cardID, BoardID: req.BoardID, Kind: req.Kind, ClockState: state,
			Actor: actor, Summary: req.Summary, Detail: req.Detail,
			OccurredAt: db.EventTime(req.OccurredAt),
		})
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, stored)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// ============================ Card notes ============================

func (a *API) cardNotes(w http.ResponseWriter, r *http.Request, cardID string) {
	switch r.Method {
	case "GET":
		notes, err := a.store.ListCardNotes(cardID, r.URL.Query().Get("board_id"))
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, notes)
	case "POST":
		var req model.CreateCardNoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if req.Actor == "" {
			req.Actor = actorFrom(r)
		}
		note, err := a.store.CreateCardNote(cardID, &req)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		// A note is an action, so it lands on the timeline. Some notes are the exact
		// moment the ball changes hands ("replied, waiting on them"), which is why a
		// note may carry its own clock state.
		state := model.ClockRunning
		if def, ok := model.DefaultClockStateForEventKind(model.EventNoteAdded); ok {
			state = def
		}
		if req.ClockState != "" {
			state = req.ClockState
		}
		if err := a.recordEvent(&model.CardEvent{
			CardID: cardID, BoardID: req.BoardID, Kind: model.EventNoteAdded, ClockState: state,
			Actor: req.Actor, Summary: note.Body, NoteID: note.ID, OccurredAt: note.CreatedAt,
		}); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 201, note)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) noteByID(w http.ResponseWriter, r *http.Request, noteID string) {
	if r.Method != "DELETE" {
		writeError(w, 405, "method not allowed")
		return
	}
	if err := a.store.DeleteCardNote(noteID); err != nil {
		mapDBErr(w, err)
		return
	}
	// The note is gone; the note_added event stays. The note was the content, the
	// event was the fact that something happened at that moment, and deleting the
	// first does not unmake the second — nor should it silently move every figure
	// computed from the timeline.
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ============================ Timeline ============================

// CardTimeline is the answer to "what happened to this card, in what order, and
// how long did each step take".
type CardTimeline struct {
	CardID  string                    `json:"card_id"`
	BoardID string                    `json:"board_id,omitempty"`
	Summary *model.CardTimeSummary    `json:"summary"`
	Entries []model.TimelineEntry     `json:"entries"`
	Notes   []model.CardNote          `json:"notes"`
	Level   *model.BoardPriorityLevel `json:"priority_level,omitempty"`
}

func (a *API) cardTimeline(w http.ResponseWriter, r *http.Request, cardID string) {
	if r.Method != "GET" {
		writeError(w, 405, "method not allowed")
		return
	}
	boardID := r.URL.Query().Get("board_id")
	// With no board named, the card's timeline is read on whichever board it sits
	// on, if it sits on exactly one. A card on several boards is asked about per
	// board, because the ladder and the working week are the board's, not the
	// card's.
	if boardID == "" {
		placements, err := a.store.ListPlacementsByCard(cardID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if len(placements) == 1 {
			boardID = placements[0].BoardID
		}
	}
	events, err := a.store.ListCardEvents(cardID, boardID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	notes, err := a.store.ListCardNotes(cardID, boardID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	notesByID := map[string]*model.CardNote{}
	for i := range notes {
		notesByID[notes[i].ID] = &notes[i]
	}

	var level *model.BoardPriorityLevel
	var hours *model.BusinessHours
	if boardID != "" {
		board, err := a.store.GetBoard(boardID)
		if err != nil && !errors.Is(err, db.ErrNotFound) {
			writeError(w, 500, err.Error())
			return
		}
		if board != nil {
			hours = board.BusinessHours
		}
		ladder, err := a.store.GetPriorityLadder(boardID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if item, err := a.noteboard.GetItem(cardID); err == nil {
			level = ladder.LevelFor(priorityOfItem(item))
		}
	}

	summary, entries := timeaccounting.Compute(timeaccounting.Input{
		Events: events, Notes: notesByID, Level: level, Hours: hours, Now: time.Now().UTC(),
	})
	writeJSON(w, 200, CardTimeline{
		CardID: cardID, BoardID: boardID, Summary: summary, Entries: entries, Notes: notes, Level: level,
	})
}

// priorityOfItem reads the noteboard priority off an opaque item. A missing or
// unreadable priority is the unranked value, which earns no rung and no limit.
func priorityOfItem(item map[string]any) int {
	if item == nil {
		return model.UnsetPriorityValue
	}
	switch v := item["priority"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return model.UnsetPriorityValue
}

// ============================ Priority ladder ============================

func (a *API) boardPriorityLevels(w http.ResponseWriter, r *http.Request, boardID string) {
	switch r.Method {
	case "GET":
		ladder, err := a.store.GetPriorityLadder(boardID)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, ladder)
	case "PUT":
		var req model.SetPriorityLadderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "invalid JSON")
			return
		}
		if err := req.Validate(); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		ladder, err := a.store.SetPriorityLadder(boardID, req.Levels)
		if err != nil {
			mapDBErr(w, err)
			return
		}
		writeJSON(w, 200, ladder)
	default:
		writeError(w, 405, "method not allowed")
	}
}

// notesByID routes /api/notes/{id}. Notes are addressed globally because a note
// belongs to a card, not to a board.
func (a *API) notesByID(w http.ResponseWriter, r *http.Request) {
	noteID := strings.TrimPrefix(r.URL.Path, "/api/notes/")
	if noteID == "" {
		writeError(w, 400, "missing note id")
		return
	}
	a.noteByID(w, r, noteID)
}
