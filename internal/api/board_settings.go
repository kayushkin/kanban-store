package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kayushkin/kanban-store/internal/bundlestore"
	"github.com/kayushkin/kanban-store/internal/config"
	"github.com/kayushkin/kanban-store/internal/llmbridge"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/principalstore"
)

// Board settings — the defaults a dispatcher or classifier reads off the board
// instead of taking as a flag on a cron job. Three of them are ids another
// store hands out, and each is asked before the write: a 400 when the owner
// says the id does not exist, a 502 when the owner could not be asked, and
// nothing written either way. The reasoning is the one card assignments
// follow (internal/principalstore): a wrong default here is not a dangling
// pointer a reader can notice, it is a board whose every dispatch fails in a
// cron log, far from the PATCH that caused it.

// settingsCheckFailure carries the status the owner's answer maps to, so the
// PATCH handler reports a wrong id (400) and an owner that is down (502)
// differently — they mean opposite things to the caller.
type settingsCheckFailure struct {
	status  int
	message string
}

func (f *settingsCheckFailure) Error() string { return f.message }

// checkBoardSettings asks each owner about every non-empty id in the request.
// An empty string is a clear and needs no owner.
func (a *API) checkBoardSettings(req *model.UpdateBoardRequest) error {
	if req.DefaultPrincipalID != nil && *req.DefaultPrincipalID != "" {
		if err := a.checkPrincipalIsAssignable(*req.DefaultPrincipalID); err != nil {
			return err
		}
	}
	if req.DefaultAgentID != nil && *req.DefaultAgentID != "" {
		if err := a.bridge.CheckAgentExists(*req.DefaultAgentID); err != nil {
			if errors.Is(err, llmbridge.ErrNotFound) {
				return &settingsCheckFailure{400, "default_agent_id: " + err.Error()}
			}
			return &settingsCheckFailure{502, "llm-bridge-server check of default_agent_id failed: " + err.Error()}
		}
	}
	if req.DefaultInstanceID != nil && *req.DefaultInstanceID != "" {
		if err := a.bridge.CheckInstanceExists(*req.DefaultInstanceID); err != nil {
			if errors.Is(err, llmbridge.ErrNotFound) {
				return &settingsCheckFailure{400, "default_instance_id: " + err.Error()}
			}
			return &settingsCheckFailure{502, "llm-bridge-server check of default_instance_id failed: " + err.Error()}
		}
	}
	if req.DefaultBundleID != nil && *req.DefaultBundleID != "" {
		if err := a.bundles.CheckBundleExists(*req.DefaultBundleID); err != nil {
			if errors.Is(err, bundlestore.ErrNotFound) {
				return &settingsCheckFailure{400, "default_bundle_id: " + err.Error()}
			}
			return &settingsCheckFailure{502, "bundle-store check of default_bundle_id failed: " + err.Error()}
		}
	}
	return nil
}

// checkPrincipalIsAssignable is the same question PUT /assignments asks, with
// the same answers: malformed or unknown or disabled is 400, unreachable is 502.
func (a *API) checkPrincipalIsAssignable(principalID string) error {
	if !principalIDShape.MatchString(principalID) {
		return &settingsCheckFailure{400, fmt.Sprintf("default_principal_id must match ^%s$ (for example principal_000001), got %q", config.PrincipalIDPattern, principalID)}
	}
	principal, err := a.principals.Get(principalID)
	if errors.Is(err, principalstore.ErrNotFound) {
		return &settingsCheckFailure{400, fmt.Sprintf("default_principal_id %s does not exist in principal-store", principalID)}
	}
	if err != nil {
		return &settingsCheckFailure{502, "principal-store check failed: " + err.Error()}
	}
	if principal.Disabled() {
		return &settingsCheckFailure{400, fmt.Sprintf("default_principal_id %s is disabled in principal-store", principalID)}
	}
	return nil
}

func writeSettingsCheckFailure(w http.ResponseWriter, err error) {
	var failure *settingsCheckFailure
	if errors.As(err, &failure) {
		writeError(w, failure.status, failure.message)
		return
	}
	writeError(w, 500, err.Error())
}

// defaultAssigneeOnArrival answers which principal a card arriving on the board
// should be handed to: the first matching tag rule that names one, else the
// board's default — resolved by model.ResolveEffectiveDefaults with the card's
// tags. The choice is re-checked with principal-store now rather than trusted
// from the day it was set, because a principal disabled since then must not be
// put on new work. Nil when nothing names a principal. The check runs BEFORE
// the card is created, so a default that has gone bad refuses the card loudly
// instead of leaving a noteboard item behind with no placement.
func (a *API) defaultAssigneeOnArrival(boardID string, cardTags []string) (*model.EffectiveDefault, error) {
	resolved, err := a.effectiveDefaultsFor(boardID, cardTags)
	if err != nil {
		return nil, err
	}
	chosen, named := resolved.Defaults[model.DefaultFieldPrincipal]
	if !named {
		return nil, nil
	}
	if err := a.checkPrincipalIsAssignable(chosen.Value); err != nil {
		var failure *settingsCheckFailure
		if errors.As(err, &failure) {
			where := fmt.Sprintf("board %s default assignee", boardID)
			fix := "clear or change the board's default_principal_id"
			if chosen.Source.Kind == model.DefaultSourceTagRule {
				where = fmt.Sprintf("board %s tag rule %s (tags %s) default assignee", boardID, chosen.Source.RuleID, strings.Join(chosen.Source.RuleTags, " + "))
				fix = fmt.Sprintf("change that rule's default_principal_id with PUT /api/boards/%s/tag-rules", boardID)
			}
			return nil, &settingsCheckFailure{failure.status, fmt.Sprintf("%s cannot be applied — %s; %s", where, failure.message, fix)}
		}
		return nil, err
	}
	return &chosen, nil
}

// applyDefaultAssignee puts the chosen default principal on the card, unless
// someone is already on it — a default fills a blank, it never overrides an
// assignment a person made. The assigned event says where the choice came
// from: the board, or which tag rule. Returns the assignments the card
// carries afterwards, for the card view the caller answers with.
func (a *API) applyDefaultAssignee(cardID string, chosen *model.EffectiveDefault, actor string) ([]model.CardAssignment, error) {
	existing, err := a.store.ListCardAssignments(cardID)
	if err != nil {
		return nil, err
	}
	if chosen == nil || len(existing) > 0 {
		return existing, nil
	}
	assignment, created, err := a.store.AssignPrincipalToCard(cardID, chosen.Value, actor)
	if err != nil {
		return nil, err
	}
	if !created {
		return existing, nil
	}
	state, err := a.clockStateCarriedForward(cardID)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"principal_id": chosen.Value, "source": "board_default"}
	if chosen.Source.Kind == model.DefaultSourceTagRule {
		detail["source"] = "tag_rule"
		detail["rule_id"] = chosen.Source.RuleID
		detail["rule_tags"] = chosen.Source.RuleTags
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return nil, err
	}
	if err := a.recordEventAndFireMessageTriggers(&model.CardEvent{
		CardID: cardID, Kind: model.EventAssigned, ClockState: state, Actor: actor,
		Summary:    chosen.Value,
		Detail:     json.RawMessage(detailJSON),
		OccurredAt: assignment.CreatedAt,
	}); err != nil {
		return nil, err
	}
	return []model.CardAssignment{*assignment}, nil
}
