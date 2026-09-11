package model

import (
	"fmt"
	"strings"
	"time"
)

// Board is a kanban board. Each board defines its own ordered set of columns.
// Cards (which live in noteboard) attach to a board via Placement rows.
type Board struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	// BusinessHours is the board's working week. Absent means the board reports
	// wall-clock time only; it is never defaulted, because a guessed zone produces
	// business figures nobody can check.
	BusinessHours *BusinessHours `json:"business_hours,omitempty"`
	// DefaultPrincipalID is principal-store's id for whoever a card on this
	// board belongs to until someone says otherwise. kanban-store applies it
	// itself, at the moment a card is created on or attached to the board, and
	// only when the card has no assignee yet — an assignment a person made is
	// never overwritten by a default. Empty means cards arrive unassigned.
	DefaultPrincipalID string `json:"default_principal_id,omitempty"`
	// DefaultAgentID is agent-store's numeric id (as llm-bridge-server's GET
	// /agents lists it, never the renameable slug) for the agent a dispatcher
	// spawns to work a card from this board. kanban-store stores and checks it;
	// the dispatcher binaries read it. Empty means the dispatcher has no
	// board-level answer and must refuse rather than guess.
	DefaultAgentID string `json:"default_agent_id,omitempty"`
	// DefaultInstanceID is the llm-bridge-server harness instance that hosts
	// sessions spawned for this board's cards. Same ownership as DefaultAgentID.
	DefaultInstanceID string `json:"default_instance_id,omitempty"`
	// Classifier says how mail becomes cards on this board. Absent means no
	// classifier files onto it. The scheduler still owns WHEN the classifier
	// runs; this is only WHAT it runs with, so the board is the one place the
	// answer lives instead of a flag on a cron job.
	Classifier *ClassifierConfig `json:"classifier,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// ClassifierConfig is what email-classifier reads off a board before filing
// mail onto it.
type ClassifierConfig struct {
	// Vocabulary names one of email-classifier's classification schemes
	// ("personal", "work", …). email-classifier owns the list; kanban-store only
	// keeps the name, and the classifier refuses a board naming one it does not
	// have. `email-classifier -list-vocabularies` prints them.
	Vocabulary string `json:"vocabulary"`
	// MailAccountIDs are mailstack's account ids (GET /api/accounts → id) the
	// classifier reads for this board. Explicit, never "every account": a
	// mailbox added later must be pointed at a board on purpose, not swept onto
	// whichever board says "all". Checked by the classifier at run time, since
	// mailstack is behind a token this store does not hold.
	MailAccountIDs []string `json:"mail_account_ids"`
	// HoldNewCards creates every card parked, so an unattended worker
	// discovering noteboard todos does not pick it up before the pipeline that
	// owns the card releases it.
	HoldNewCards bool `json:"hold_new_cards"`
}

// Validate refuses a config that would send the classifier looking for nothing.
func (c *ClassifierConfig) Validate() error {
	if strings.TrimSpace(c.Vocabulary) == "" {
		return fmt.Errorf("classifier.vocabulary is required: name one of email-classifier's vocabularies")
	}
	if len(c.MailAccountIDs) == 0 {
		return fmt.Errorf("classifier.mail_account_ids is required: at least one mailstack account id, as GET /api/accounts lists them")
	}
	for _, id := range c.MailAccountIDs {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("classifier.mail_account_ids has an empty or untrimmed entry %q", id)
		}
	}
	return nil
}

// Cleared reports whether this is the present-but-empty object a PATCH sends to
// remove the classifier, mirroring business_hours' empty tzid.
func (c *ClassifierConfig) Cleared() bool {
	return c.Vocabulary == "" && len(c.MailAccountIDs) == 0 && !c.HoldNewCards
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
	// BusinessHours replaces the board's working week. Sending an object with an
	// empty tzid clears it; omitting the field leaves it alone.
	BusinessHours *BusinessHours `json:"business_hours,omitempty"`
	// The three default ids: a present empty string clears the field, omitting
	// it leaves it alone. The API checks a non-empty value with its owner
	// (principal-store, llm-bridge-server) before anything is written.
	DefaultPrincipalID *string `json:"default_principal_id,omitempty"`
	DefaultAgentID     *string `json:"default_agent_id,omitempty"`
	DefaultInstanceID  *string `json:"default_instance_id,omitempty"`
	// Classifier replaces the board's classifier config. Sending an empty
	// object clears it; omitting the field leaves it alone.
	Classifier *ClassifierConfig `json:"classifier,omitempty"`
}

func (r *UpdateBoardRequest) Validate() error {
	if r.BusinessHours != nil && r.BusinessHours.TZID != "" {
		if err := r.BusinessHours.Validate(); err != nil {
			return err
		}
	}
	if r.Classifier != nil && !r.Classifier.Cleared() {
		if err := r.Classifier.Validate(); err != nil {
			return err
		}
	}
	for name, value := range map[string]*string{
		"default_principal_id": r.DefaultPrincipalID,
		"default_agent_id":     r.DefaultAgentID,
		"default_instance_id":  r.DefaultInstanceID,
	} {
		if value != nil && *value != strings.TrimSpace(*value) {
			return fmt.Errorf("%s %q has surrounding whitespace, and nothing is trimmed: send the owner's id exactly", name, *value)
		}
	}
	return nil
}

// Column belongs to a Board. Position is a float so reorders are cheap
// (insert between two columns by averaging their positions). AutoStatus, when
// set, causes a card move into this column to PATCH the noteboard item's
// status to that value.
type Column struct {
	ID         string  `json:"id"`
	BoardID    string  `json:"board_id"`
	Name       string  `json:"name"`
	Position   float64 `json:"position"`
	Color      string  `json:"color"`
	WIPLimit   *int    `json:"wip_limit,omitempty"`
	AutoStatus *string `json:"auto_status,omitempty"`
	// BudgetClockState is what landing in this column means for the budget clock:
	// "In Progress" runs it, "Blocked" pauses it, "Done" stops it. Absent means a
	// move here says nothing about the clock and leaves it as it was — which is the
	// honest default for a column nobody has classified.
	BudgetClockState *ClockState `json:"budget_clock_state,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	UpdatedAt        time.Time   `json:"updated_at"`
}

type CreateColumnRequest struct {
	Name             string      `json:"name"`
	Position         *float64    `json:"position,omitempty"`
	Color            *string     `json:"color,omitempty"`
	WIPLimit         *int        `json:"wip_limit,omitempty"`
	AutoStatus       *string     `json:"auto_status,omitempty"`
	BudgetClockState *ClockState `json:"budget_clock_state,omitempty"`
}

func (r *CreateColumnRequest) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.AutoStatus != nil && !validStatus(*r.AutoStatus) {
		return fmt.Errorf("auto_status must be one of: open, done, archived")
	}
	if r.BudgetClockState != nil && *r.BudgetClockState != "" && !ValidClockState(*r.BudgetClockState) {
		return fmt.Errorf("budget_clock_state must be one of: %s", strings.Join(ClockStateNames(), ", "))
	}
	return nil
}

type UpdateColumnRequest struct {
	Name       *string  `json:"name,omitempty"`
	Position   *float64 `json:"position,omitempty"`
	Color      *string  `json:"color,omitempty"`
	WIPLimit   *int     `json:"wip_limit,omitempty"`
	AutoStatus *string  `json:"auto_status,omitempty"`
	// BudgetClockState reclassifies the column. An empty string clears it.
	BudgetClockState *ClockState `json:"budget_clock_state,omitempty"`
}

func (r *UpdateColumnRequest) Validate() error {
	if r.AutoStatus != nil && *r.AutoStatus != "" && !validStatus(*r.AutoStatus) {
		return fmt.Errorf("auto_status must be one of: open, done, archived")
	}
	if r.BudgetClockState != nil && *r.BudgetClockState != "" && !ValidClockState(*r.BudgetClockState) {
		return fmt.Errorf("budget_clock_state must be one of: %s", strings.Join(ClockStateNames(), ", "))
	}
	return nil
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
	// ParentID makes this card a child of another item. The hold gate and the
	// spend ceiling roll up over this edge, so a sub-card created without it
	// escapes its parent's hold and its parent's spend ceiling.
	ParentID *string `json:"parent_id,omitempty"`
	// Hold creates the card parked — agents cannot pick the work up until a human
	// presses play. Opt-in, for classes of work that should never run unattended.
	Hold       bool   `json:"hold,omitempty"`
	HoldReason string `json:"hold_reason,omitempty"`
	// AutoHoldAtUSD is the spend ceiling: hold this card once its agent sessions
	// have cost this much in total. Nil = no ceiling. Zero is a REAL ceiling, so
	// this is a pointer — a plain float64 would make "no ceiling" and "stop before
	// spending a cent" the same request.
	AutoHoldAtUSD *float64 `json:"auto_hold_at_usd,omitempty"`
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
	// OccurredAt backdates the ACTION a link records, for the link types that
	// record one. It is the same field, for the same reason, as the one on
	// CreateCardEventRequest: a classifier reads a mailbox on a cadence, so the
	// mail it files arrived before anything here heard about it. Without this,
	// attaching week-old mail reported it as arriving now, and every card built
	// by filing a backlog carried a timeline that began the moment it was filed.
	//
	// It does NOT move the link's own CreatedAt. When the link was recorded and
	// when the thing happened are two different facts, and the event type here
	// already keeps them apart as RecordedAt and OccurredAt.
	//
	// Sending it with a link type that records no action is an error rather than
	// a no-op — see the handler. Silently ignoring it would let a caller believe
	// it had backdated something it had not.
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	// ClockState overrides what the arrival means for the budget clock.
	//
	// The link type already implies one — mail arriving is work landing in front
	// of you, so it runs the clock. That is right for a card someone works, and
	// wrong for a bucket: a hundred build digests filed on one No-action card
	// are not a hundred arrivals of work, and letting them run the clock reports
	// a card nobody has ever touched as having consumed weeks of budget.
	//
	// Like OccurredAt, it is refused on a link type that records no action.
	ClockState ClockState `json:"clock_state,omitempty"`
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

// CardAssignment says a principal is on a card. It is its own fact about the
// card, not a card link: a link's label is a display name and its uniqueness is
// per entity ref, and neither is what "assigned" means. PrincipalID is an id
// minted by principal-store, which owns who a principal is; kanban-store checks
// it exists there before writing the row and never stores the name.
//
// There is no role. Version one answers "who is assigned"; owner versus
// reviewer is a later question, if it is ever asked.
type CardAssignment struct {
	CardID      string    `json:"card_id"`
	PrincipalID string    `json:"principal_id"`
	AssignedBy  string    `json:"assigned_by"`
	CreatedAt   time.Time `json:"created_at"`
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
	Board   *Board       `json:"board"`
	Columns []ColumnView `json:"columns"`
	Orphans []CardView   `json:"orphans,omitempty"`
	// PriorityLadder is the board's rungs, so a client can label and colour a card
	// without asking a second time. An empty ladder means this board ignores
	// priorities.
	PriorityLadder *PriorityLadder `json:"priority_ladder,omitempty"`
}

type ColumnView struct {
	Column *Column    `json:"column"`
	Cards  []CardView `json:"cards"`
	// Total is how many cards the column holds, which is not len(Cards) once a
	// board view has been capped. A client needs both to say "showing 25 of 6,466"
	// rather than quietly presenting a page as the whole column.
	Total int `json:"total"`
}

// CardView combines a placement with its noteboard item content (passed
// through unchanged), links and assignments. Item is `any` so we don't
// redefine noteboard's Item shape here.
type CardView struct {
	Placement   *Placement       `json:"placement"`
	Item        any              `json:"item"`
	Links       []CardLink       `json:"links,omitempty"`
	Assignments []CardAssignment `json:"assignments,omitempty"`
	// Time is the card's clock as the board sees it: how long it has been alive,
	// how much of that counted as workable, and how that sits against the limit
	// its priority sets. Computed from the card's events on every read, never
	// stored.
	Time *CardTimeSummary `json:"time,omitempty"`
}

type EntityTypeInfo struct {
	Type    string `json:"type"`
	Service string `json:"service,omitempty"`
	Search  string `json:"search,omitempty"`
	// Get is the owning service's fetch-one-by-id route, relative to the
	// service root, with "{id}" marking where the id goes (e.g.
	// "/api/items/{id}"). Empty means the service has no by-id lookup for this
	// type, so a reference resolver cannot probe it.
	Get string `json:"get,omitempty"`
	// IDPatterns are case-insensitive regular expressions describing what this
	// type's ids look like, matched against the WHOLE candidate id (resolvers
	// anchor them). A reference resolver probes an id against every type whose
	// pattern matches. Empty means ids of this type have no recognizable shape
	// and are never probed.
	IDPatterns []string `json:"id_patterns,omitempty"`
}

type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}
