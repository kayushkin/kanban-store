package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kayushkin/kanban-store/internal/model"
)

// migrateActivity adds the tables that make a card's history readable: the event
// log every figure is computed from, the notes written onto cards, and each
// board's priority ladder.
//
// It also widens two existing tables. Both ALTERs are guarded by a lookup rather
// than by swallowing the "duplicate column" error, so a genuinely failed
// migration still surfaces instead of being mistaken for one that already ran.
func migrateActivity(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS card_events (
			id             TEXT PRIMARY KEY,
			card_id        TEXT NOT NULL,
			board_id       TEXT NOT NULL DEFAULT '',
			kind           TEXT NOT NULL,
			clock_state    TEXT NOT NULL,
			actor          TEXT NOT NULL DEFAULT '',
			summary        TEXT NOT NULL DEFAULT '',
			from_column_id TEXT NOT NULL DEFAULT '',
			to_column_id   TEXT NOT NULL DEFAULT '',
			note_id        TEXT NOT NULL DEFAULT '',
			detail         TEXT NOT NULL DEFAULT '',
			occurred_at    DATETIME NOT NULL,
			recorded_at    DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_card_events_card  ON card_events(card_id, occurred_at);
		CREATE INDEX IF NOT EXISTS idx_card_events_board ON card_events(board_id, occurred_at);

		CREATE TABLE IF NOT EXISTS card_notes (
			id         TEXT PRIMARY KEY,
			card_id    TEXT NOT NULL,
			board_id   TEXT NOT NULL DEFAULT '',
			kind       TEXT NOT NULL DEFAULT 'note',
			body       TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_card_notes_card ON card_notes(card_id, created_at);

		CREATE TABLE IF NOT EXISTS board_priority_levels (
			board_id       TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
			priority_value INTEGER NOT NULL,
			label          TEXT NOT NULL,
			budget_seconds INTEGER,
			PRIMARY KEY (board_id, priority_value)
		);
	`)
	if err != nil {
		return err
	}
	if err := addColumnIfMissing(db, "boards", "business_hours", "TEXT"); err != nil {
		return err
	}
	return addColumnIfMissing(db, "columns", "budget_clock_state", "TEXT")
}

func addColumnIfMissing(db *sql.DB, table, column, decl string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl))
	return err
}

// clockStateValue renders a column's clock setting for storage: an unset one is
// stored as NULL, not as an empty string that would read back as a state.
func clockStateValue(s *model.ClockState) any {
	if s == nil || *s == "" {
		return nil
	}
	return string(*s)
}

// ============================ Card events ============================

// RecordCardEvent appends one action to the log. The log is append-only by
// design: there is no update path, because rewriting an event would silently
// restate a card's history and every figure derived from it.
func (s *Store) RecordCardEvent(e *model.CardEvent) (*model.CardEvent, error) {
	if e.CardID == "" {
		return nil, errors.New("card_id is required")
	}
	if e.Kind == "" {
		return nil, errors.New("kind is required")
	}
	if !model.ValidClockState(e.ClockState) {
		return nil, fmt.Errorf("invalid clock_state %q", e.ClockState)
	}
	stored := *e
	stored.ID = uuid.NewString()
	stored.RecordedAt = now()
	if stored.OccurredAt.IsZero() {
		stored.OccurredAt = stored.RecordedAt
	}
	stored.OccurredAt = stored.OccurredAt.UTC()
	detail := ""
	if len(stored.Detail) > 0 {
		detail = string(stored.Detail)
	}
	_, err := s.db.Exec(
		`INSERT INTO card_events
		   (id, card_id, board_id, kind, clock_state, actor, summary, from_column_id, to_column_id, note_id, detail, occurred_at, recorded_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stored.ID, stored.CardID, stored.BoardID, string(stored.Kind), string(stored.ClockState),
		stored.Actor, stored.Summary, stored.FromColumnID, stored.ToColumnID, stored.NoteID,
		detail, stored.OccurredAt, stored.RecordedAt,
	)
	if err != nil {
		return nil, err
	}
	return &stored, nil
}

const cardEventColumns = `id, card_id, board_id, kind, clock_state, actor, summary,
	from_column_id, to_column_id, note_id, detail, occurred_at, recorded_at`

func scanCardEvent(r scanner) (model.CardEvent, error) {
	var e model.CardEvent
	var kind, state, detail string
	if err := r.Scan(&e.ID, &e.CardID, &e.BoardID, &kind, &state, &e.Actor, &e.Summary,
		&e.FromColumnID, &e.ToColumnID, &e.NoteID, &detail, &e.OccurredAt, &e.RecordedAt); err != nil {
		return e, err
	}
	e.Kind = model.EventKind(kind)
	e.ClockState = model.ClockState(state)
	if detail != "" {
		e.Detail = json.RawMessage(detail)
	}
	return e, nil
}

// ListCardEvents returns a card's actions oldest first.
//
// boardID scopes the answer to one board's timeline: board-specific actions on
// that board, plus the card-wide ones — an email arriving concerns the card
// wherever it sits, so it belongs on every board's timeline. An empty boardID
// returns everything.
func (s *Store) ListCardEvents(cardID, boardID string) ([]model.CardEvent, error) {
	q := `SELECT ` + cardEventColumns + ` FROM card_events WHERE card_id = ?`
	args := []any{cardID}
	if boardID != "" {
		q += ` AND (board_id = ? OR board_id = '')`
		args = append(args, boardID)
	}
	q += ` ORDER BY occurred_at ASC, recorded_at ASC, id ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CardEvent{}
	for rows.Next() {
		e, err := scanCardEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListCardEventsForBoard fetches the events of every card placed on a board in
// one query, grouped by card. Assembling a board otherwise means one query per
// card, which is the shape that already makes the links fan-out expensive here.
func (s *Store) ListCardEventsForBoard(boardID string) (map[string][]model.CardEvent, error) {
	rows, err := s.db.Query(
		`SELECT `+cardEventColumns+` FROM card_events
		 WHERE (board_id = ? OR board_id = '')
		   AND card_id IN (SELECT card_id FROM placements WHERE board_id = ?)
		 ORDER BY occurred_at ASC, recorded_at ASC, id ASC`, boardID, boardID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]model.CardEvent{}
	for rows.Next() {
		e, err := scanCardEvent(rows)
		if err != nil {
			return nil, err
		}
		out[e.CardID] = append(out[e.CardID], e)
	}
	return out, rows.Err()
}

// ============================ Card notes ============================

func (s *Store) CreateCardNote(cardID string, req *model.CreateCardNoteRequest) (*model.CardNote, error) {
	n := &model.CardNote{
		ID:        uuid.NewString(),
		CardID:    cardID,
		BoardID:   req.BoardID,
		Kind:      req.Kind,
		Body:      req.Body,
		Actor:     req.Actor,
		CreatedAt: now(),
	}
	if n.Kind == "" {
		n.Kind = "note"
	}
	if req.OccurredAt != nil {
		n.CreatedAt = req.OccurredAt.UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO card_notes (id, card_id, board_id, kind, body, actor, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.CardID, n.BoardID, n.Kind, n.Body, n.Actor, n.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (s *Store) ListCardNotes(cardID, boardID string) ([]model.CardNote, error) {
	q := `SELECT id, card_id, board_id, kind, body, actor, created_at FROM card_notes WHERE card_id = ?`
	args := []any{cardID}
	if boardID != "" {
		q += ` AND (board_id = ? OR board_id = '')`
		args = append(args, boardID)
	}
	q += ` ORDER BY created_at ASC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.CardNote{}
	for rows.Next() {
		var n model.CardNote
		if err := rows.Scan(&n.ID, &n.CardID, &n.BoardID, &n.Kind, &n.Body, &n.Actor, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCardNote(noteID string) error {
	res, err := s.db.Exec(`DELETE FROM card_notes WHERE id = ?`, noteID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ============================ Priority ladder ============================

// GetPriorityLadder returns a board's rungs, top rung first — and the top rung is
// the highest priority_value, because that is the end of noteboard's scale the
// urgent work sits at.
func (s *Store) GetPriorityLadder(boardID string) (*model.PriorityLadder, error) {
	rows, err := s.db.Query(
		`SELECT board_id, priority_value, label, budget_seconds FROM board_priority_levels
		 WHERE board_id = ? ORDER BY priority_value DESC`, boardID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ladder := &model.PriorityLadder{BoardID: boardID, Levels: []model.BoardPriorityLevel{}}
	for rows.Next() {
		var lv model.BoardPriorityLevel
		var budget sql.NullInt64
		if err := rows.Scan(&lv.BoardID, &lv.PriorityValue, &lv.Label, &budget); err != nil {
			return nil, err
		}
		if budget.Valid {
			v := int(budget.Int64)
			lv.BudgetSeconds = &v
		}
		ladder.Levels = append(ladder.Levels, lv)
	}
	return ladder, rows.Err()
}

// SetPriorityLadder replaces a board's ladder wholesale. Rungs only mean anything
// relative to each other, so there is no way to edit one in isolation. An empty
// set of levels is a legitimate ladder: it means the board ignores priorities.
func (s *Store) SetPriorityLadder(boardID string, levels []model.BoardPriorityLevel) (*model.PriorityLadder, error) {
	if _, err := s.GetBoard(boardID); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM board_priority_levels WHERE board_id = ?`, boardID); err != nil {
		return nil, err
	}
	for _, lv := range levels {
		var budget any
		if lv.BudgetSeconds != nil {
			budget = *lv.BudgetSeconds
		}
		if _, err := tx.Exec(
			`INSERT INTO board_priority_levels (board_id, priority_value, label, budget_seconds) VALUES (?, ?, ?, ?)`,
			boardID, lv.PriorityValue, lv.Label, budget,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPriorityLadder(boardID)
}

// ============================ Time helpers ============================

// EventTime picks the moment an action happened, defaulting to now. Callers hand
// it a caller-supplied timestamp that may be nil.
func EventTime(at *time.Time) time.Time {
	if at == nil {
		return now()
	}
	return at.UTC()
}
