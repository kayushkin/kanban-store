package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/kayushkin/kanban-store/internal/config"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/model"
	"github.com/kayushkin/kanban-store/internal/principalstore"
)

// The ticket routes: the two facts a card cannot hold, and the lifecycle it
// does not store. See internal/model/ticket.go for why there is no status
// field here.

// ticketChannels serves the channel vocabulary, so no caller builds a picker
// out of whichever channels happen to have been used so far.
func (a *API) ticketChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, model.TicketChannels)
}

// ticketLifecycleStates serves the lifecycle vocabulary. A column is
// classified with one of these; a ticket reads its state from its column.
func (a *API) ticketLifecycleStates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, model.TicketLifecycleStates)
}

// cardTicket is GET | PUT | DELETE /api/cards/{id}/ticket.
func (a *API) cardTicket(w http.ResponseWriter, r *http.Request, cardID string) {
	switch r.Method {
	case http.MethodGet:
		view, err := a.ticketView(cardID)
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, 404, "card "+cardID+" is not a ticket")
			return
		}
		if err != nil {
			writeTicketError(w, err)
			return
		}
		writeJSON(w, 200, view)
	case http.MethodPut:
		a.putCardTicket(w, r, cardID)
	case http.MethodDelete:
		if err := a.store.DeleteTicket(cardID); err != nil {
			mapDBErr(w, err)
			return
		}
		// The card stays exactly where it is: "this is not a ticket" is not
		// "this is not work".
		writeJSON(w, 200, map[string]string{"status": "ok"})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *API) putCardTicket(w http.ResponseWriter, r *http.Request, cardID string) {
	var request model.TicketWriteRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return
	}
	channel, known := model.NormalizeTicketChannel(request.Channel)
	if !known {
		writeError(w, 400, model.ErrUnknownTicketChannel(request.Channel).Error())
		return
	}
	if !principalIDShape.MatchString(request.RequesterPrincipalID) {
		writeError(w, 400, fmt.Sprintf("requester_principal_id must match ^%s$ (for example principal_000010), got %q",
			config.PrincipalIDPattern, request.RequesterPrincipalID))
		return
	}
	requester, err := a.principals.Get(request.RequesterPrincipalID)
	if errors.Is(err, principalstore.ErrNotFound) {
		writeError(w, 400, fmt.Sprintf("requester_principal_id %s does not exist in principal-store", request.RequesterPrincipalID))
		return
	}
	if err != nil {
		// The check failed, so the write does not happen: a ticket pointing at
		// a requester nobody confirmed is the silently wrong row this check
		// exists to prevent.
		writeError(w, 502, "principal-store check failed: "+err.Error())
		return
	}
	if requester.Disabled() {
		writeError(w, 400, fmt.Sprintf("requester_principal_id %s is disabled in principal-store", request.RequesterPrincipalID))
		return
	}
	// The requester is the outside party. A human or a group here would mean a
	// member of staff had been recorded as the person who asked, and every
	// later "who reported this?" would answer with a colleague.
	if requester.Kind != principalstore.KindContact {
		writeError(w, 400, fmt.Sprintf(
			"requester_principal_id %s is a %q principal; a requester is a %q — resolve the address with principal-store's POST /contacts/resolve and use the id it answers",
			request.RequesterPrincipalID, requester.Kind, principalstore.KindContact))
		return
	}

	if _, err := a.store.UpsertTicket(cardID, request.RequesterPrincipalID, channel); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	view, err := a.ticketView(cardID)
	if err != nil {
		writeTicketError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

// ticketView assembles the row, the requester's display name and one lifecycle
// per board the card sits on.
func (a *API) ticketView(cardID string) (*model.TicketView, error) {
	ticket, err := a.store.GetTicket(cardID)
	if err != nil {
		return nil, err
	}
	states, err := a.store.TicketPlacementStates(cardID)
	if err != nil {
		return nil, err
	}
	view := &model.TicketView{Ticket: ticket, States: states}
	// The name is for rendering only, and a principal-store that cannot answer
	// must not fail the read: the ticket is still the ticket without it.
	if requester, err := a.principals.Get(ticket.RequesterPrincipalID); err == nil {
		view.RequesterDisplayName = requester.DisplayName
	}
	return view, nil
}

func writeTicketError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, 404, "not found")
		return
	}
	writeError(w, 500, err.Error())
}
