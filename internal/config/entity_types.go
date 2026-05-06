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
}
