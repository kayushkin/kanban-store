package model

import (
	"fmt"
	"time"
)

// Board is a kanban board. Each board defines its own ordered set of columns.
// Cards (which live in noteboard) attach to a board via Placement rows.
type Board struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Archived    bool      `json:"archived"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateBoardRequest struct {
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

func (r *CreateBoardRequest) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

type UpdateBoardRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Archived    *bool   `json:"archived,omitempty"`
}

// Column belongs to a Board. Position is a float so reorders are cheap
// (insert between two columns by averaging their positions). AutoStatus, when
// set, causes a card move into this column to PATCH the noteboard item's
// status to that value.
type Column struct {
	ID         string    `json:"id"`
	BoardID    string    `json:"board_id"`
	Name       string    `json:"name"`
	Position   float64   `json:"position"`
	Color      string    `json:"color"`
	WIPLimit   *int      `json:"wip_limit,omitempty"`
	AutoStatus *string   `json:"auto_status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type CreateColumnRequest struct {
	Name       string   `json:"name"`
	Position   *float64 `json:"position,omitempty"`
	Color      *string  `json:"color,omitempty"`
	WIPLimit   *int     `json:"wip_limit,omitempty"`
	AutoStatus *string  `json:"auto_status,omitempty"`
}

func (r *CreateColumnRequest) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.AutoStatus != nil && !validStatus(*r.AutoStatus) {
		return fmt.Errorf("auto_status must be one of: open, done, archived")
	}
	return nil
}

type UpdateColumnRequest struct {
	Name       *string  `json:"name,omitempty"`
	Position   *float64 `json:"position,omitempty"`
	Color      *string  `json:"color,omitempty"`
	WIPLimit   *int     `json:"wip_limit,omitempty"`
	AutoStatus *string  `json:"auto_status,omitempty"`
}

func validStatus(s string) bool {
	return s == "open" || s == "done" || s == "archived"
}

// Placement attaches a noteboard item (the card) to a board+column at a position.
// Many-to-many: the same card_id can appear in multiple boards.
type Placement struct {
	CardID    string    `json:"card_id"`
	BoardID   string    `json:"board_id"`
	ColumnID  string    `json:"column_id"`
	Position  float64   `json:"position"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateCardRequest is the kanban-side payload. Title is required; remaining
// fields pass through to noteboard. ColumnID is required so the new card lands
// somewhere on the board.
type CreateCardRequest struct {
	Title    string   `json:"title"`
	Body     *string  `json:"body,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Priority *int     `json:"priority,omitempty"`
	ListID   *string  `json:"list_id,omitempty"`
	DueAt    *string  `json:"due_at,omitempty"`
	ColumnID string   `json:"column_id"`
	Position *float64 `json:"position,omitempty"`
}

func (r *CreateCardRequest) Validate() error {
	if r.Title == "" {
		return fmt.Errorf("title is required")
	}
	if r.ColumnID == "" {
		return fmt.Errorf("column_id is required")
	}
	return nil
}

// AttachCardRequest puts an existing noteboard item onto a board.
type AttachCardRequest struct {
	ColumnID string   `json:"column_id"`
	Position *float64 `json:"position,omitempty"`
}

func (r *AttachCardRequest) Validate() error {
	if r.ColumnID == "" {
		return fmt.Errorf("column_id is required")
	}
	return nil
}

// MoveCardRequest moves a card within or across columns of a single board.
type MoveCardRequest struct {
	BoardID  string  `json:"board_id"`
	ColumnID string  `json:"column_id"`
	Position float64 `json:"position"`
}

func (r *MoveCardRequest) Validate() error {
	if r.BoardID == "" {
		return fmt.Errorf("board_id is required")
	}
	if r.ColumnID == "" {
		return fmt.Errorf("column_id is required")
	}
	return nil
}

// CardLink points from a card to an external entity (session, instance,
// machine, repo, git_repo, skill, tool, …). EntityType is open — kanban-store
// does not validate that EntityRef exists in the upstream service.
type CardLink struct {
	ID         string    `json:"id"`
	CardID     string    `json:"card_id"`
	EntityType string    `json:"entity_type"`
	EntityRef  string    `json:"entity_ref"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateCardLinkRequest struct {
	EntityType string  `json:"entity_type"`
	EntityRef  string  `json:"entity_ref"`
	Label      *string `json:"label,omitempty"`
}

func (r *CreateCardLinkRequest) Validate() error {
	if r.EntityType == "" {
		return fmt.Errorf("entity_type is required")
	}
	if r.EntityRef == "" {
		return fmt.Errorf("entity_ref is required")
	}
	return nil
}

// EntityTag is cross-cutting: tag any (entity_type, entity_ref) pair without
// involving cards or boards. Lets agents tag a session "p0" or a machine "lab".
type EntityTag struct {
	EntityType string    `json:"entity_type"`
	EntityRef  string    `json:"entity_ref"`
	Tag        string    `json:"tag"`
	CreatedAt  time.Time `json:"created_at"`
}

type CreateEntityTagRequest struct {
	Tag string `json:"tag"`
}

func (r *CreateEntityTagRequest) Validate() error {
	if r.Tag == "" {
		return fmt.Errorf("tag is required")
	}
	return nil
}

// BoardView is the assembled response for GET /api/boards/:id/cards.
// Cards are joined with their noteboard content; columns carry their cards
// in placement order.
type BoardView struct {
	Board   *Board         `json:"board"`
	Columns []ColumnView   `json:"columns"`
	Orphans []CardView     `json:"orphans,omitempty"`
}

type ColumnView struct {
	Column *Column    `json:"column"`
	Cards  []CardView `json:"cards"`
}

// CardView combines a placement with its noteboard item content (passed
// through unchanged) and links. Item is `any` so we don't redefine
// noteboard's Item shape here.
type CardView struct {
	Placement *Placement `json:"placement"`
	Item      any        `json:"item"`
	Links     []CardLink `json:"links,omitempty"`
}

type EntityTypeInfo struct {
	Type    string `json:"type"`
	Service string `json:"service,omitempty"`
	Search  string `json:"search,omitempty"`
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}
