// Package config holds static, host-agnostic configuration. The entity-type
// registry lets agents self-discover where to look up entity refs they find
// in card_links / entity_tags. kanban-store does NOT proxy to these services
// (dumb store) — it just publishes the map.
package config

import "github.com/kayushkin/kanban-store/internal/model"

// EntityTypes is the canonical registry. Add new types here as needed.
// Empty Service/Search means kanban knows about the type but there is no
// upstream service to autocomplete against (e.g. git_repo URLs).
var EntityTypes = []model.EntityTypeInfo{
	{Type: "session", Service: "llm-bridge-server", Search: "/api/sessions?q="},
	{Type: "instance", Service: "llm-bridge-server", Search: "/api/instances?q="},
	{Type: "machine", Service: "healthcheck", Search: "/api/services?q="},
	{Type: "service", Service: "healthcheck", Search: "/api/services?q="},
	{Type: "skill", Service: "skill-store", Search: "/skills?q="},
	{Type: "tool", Service: "tool-store", Search: "/tools?q="},
	{Type: "repo"},     // local filesystem path; no upstream
	{Type: "git_repo"}, // remote URL; no upstream
	{Type: "agent", Service: "agent-store", Search: "/api/agents?q="},
	{Type: "note", Service: "noteboard", Search: "/api/items?q="},

	// Email. Three types rather than one, because the two ids a message has do
	// different jobs and neither can stand in for the other.
	//
	// email       — ref is "<account_id>:<provider message id>", the only id
	//               mailstack can resolve back to a message. Account-qualified
	//               because an IMAP ref is a bare UID, unique only within one
	//               mailbox.
	// email_msgid — ref is the RFC 5322 Message-ID with its angle brackets
	//               stripped. Assigned by the sender and immutable across folder
	//               moves, so it is what dedups one message that reached two
	//               accounts and what rebuilds a thread; mailstack parses the
	//               provider thread id and then drops it, leaving this as the
	//               only route to conversation grouping.
	// email_sender— ref is a lowercased sender address. Records that a sender's
	//               mail belongs on a card, which is how email-classifier learns
	//               to file bulk mail without calling a model.
	//
	// Search is empty on all three deliberately: mailstack publishes no ?q=
	// search route (GET /api/search is a 404), so there is no autocomplete to
	// advertise. It does resolve some of these refs by exact id — email via
	// GET /api/messages/{id}?account=, email_msgid via
	// GET /api/lookup/message-id/{messageID} — but an exact-id resolver is not a
	// search endpoint and does not belong in this field. Adding more resolvers
	// upstream does not change that; only a ?q= route would.
	{Type: "email", Service: "mailstack"},
	{Type: "email_msgid", Service: "mailstack"},
	{Type: "email_sender", Service: "mailstack"},
}
