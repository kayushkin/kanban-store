package db

import (
	"database/sql"
	"errors"

	"github.com/kayushkin/kanban-store/internal/model"
)

// The ticket row and the lifecycle read.
//
// One row per card, created when a card becomes a ticket and deleted when it
// stops being one. The card outlives both: deleting a ticket leaves the card
// where it is, because "this is not a ticket" is not "this is not work".

// ensureTicketTables is called from the schema path; see db.go.
func ensureTicketTables(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS tickets (
			card_id                TEXT PRIMARY KEY,
			requester_principal_id TEXT NOT NULL,
			channel                TEXT NOT NULL,
			created_at             DATETIME NOT NULL,
			updated_at             DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_tickets_requester ON tickets(requester_principal_id);
	`); err != nil {
		return err
	}
	// A column's lifecycle state: nullable, because a board that is not a
	// ticket board has no answer and must not be given one.
	return addColumnIfMissing(db, "columns", "lifecycle_state", "TEXT")
}

// UpsertTicket writes the ticket for a card. The caller has already checked
// the requester with principal-store and the channel against the vocabulary;
// this is the write.
func (s *Store) UpsertTicket(cardID string, requesterPrincipalID string, channel model.TicketChannel) (*model.Ticket, error) {
	timestamp := now()
	_, err := s.db.Exec(`
		INSERT INTO tickets (card_id, requester_principal_id, channel, created_at, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(card_id) DO UPDATE SET
			requester_principal_id = excluded.requester_principal_id,
			channel                = excluded.channel,
			updated_at             = excluded.updated_at`,
		cardID, requesterPrincipalID, string(channel), timestamp, timestamp)
	if err != nil {
		return nil, err
	}
	return s.GetTicket(cardID)
}

// GetTicket returns one card's ticket row, or ErrNotFound when the card is not
// a ticket.
func (s *Store) GetTicket(cardID string) (*model.Ticket, error) {
	ticket := &model.Ticket{}
	var channel string
	err := s.db.QueryRow(
		`SELECT card_id, requester_principal_id, channel, created_at, updated_at FROM tickets WHERE card_id = ?`, cardID).
		Scan(&ticket.CardID, &ticket.RequesterPrincipalID, &channel, &ticket.CreatedAt, &ticket.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ticket.Channel = model.TicketChannel(channel)
	return ticket, nil
}

// DeleteTicket makes a card an ordinary card again. Missing is ErrNotFound
// rather than a silent success: a caller deleting a ticket that is not there
// has the wrong card id, and should hear so.
func (s *Store) DeleteTicket(cardID string) error {
	result, err := s.db.Exec(`DELETE FROM tickets WHERE card_id = ?`, cardID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTicketsForCards reads the ticket of each card in one query, for a board
// view that must not do one read per card.
func (s *Store) ListTicketsForCards(cardIDs []string) (map[string]*model.Ticket, error) {
	out := map[string]*model.Ticket{}
	if len(cardIDs) == 0 {
		return out, nil
	}
	query := `SELECT card_id, requester_principal_id, channel, created_at, updated_at FROM tickets WHERE card_id IN (` +
		placeholders(len(cardIDs)) + `)`
	args := make([]any, 0, len(cardIDs))
	for _, id := range cardIDs {
		args = append(args, id)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		ticket := &model.Ticket{}
		var channel string
		if err := rows.Scan(&ticket.CardID, &ticket.RequesterPrincipalID, &channel, &ticket.CreatedAt, &ticket.UpdatedAt); err != nil {
			return nil, err
		}
		ticket.Channel = model.TicketChannel(channel)
		out[ticket.CardID] = ticket
	}
	return out, rows.Err()
}

// TicketPlacementStates reads where the card sits on every board it is on, and
// what each of those columns means for a ticket. See model.TicketView for why
// this answers a list rather than one state.
func (s *Store) TicketPlacementStates(cardID string) ([]model.TicketPlacementState, error) {
	rows, err := s.db.Query(`
		SELECT p.board_id, p.column_id, c.name, c.lifecycle_state
		FROM placements p JOIN columns c ON c.id = p.column_id
		WHERE p.card_id = ?
		ORDER BY p.board_id`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []model.TicketPlacementState{}
	for rows.Next() {
		var state model.TicketPlacementState
		var lifecycle sql.NullString
		if err := rows.Scan(&state.BoardID, &state.ColumnID, &state.ColumnName, &lifecycle); err != nil {
			return nil, err
		}
		if lifecycle.Valid && lifecycle.String != "" {
			value := model.TicketLifecycleState(lifecycle.String)
			state.LifecycleState = &value
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

// lifecycleStateValue renders a column's ticket lifecycle for storage; absent
// is stored as NULL, which is what "this column is not classified" means.
func lifecycleStateValue(state *model.TicketLifecycleState) any {
	if state == nil || *state == "" {
		return nil
	}
	return string(*state)
}

// lifecycleStateFromRequest reads the field a create or update request
// carries. The request validated the word already; an empty string clears.
func lifecycleStateFromRequest(raw *string) *model.TicketLifecycleState {
	if raw == nil || *raw == "" {
		return nil
	}
	state := model.TicketLifecycleState(*raw)
	return &state
}
