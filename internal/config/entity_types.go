// Package config holds the service's configuration. settings.go declares every
// environment variable the process reads, once, with llm-bridge
// servicesettings: the command reads its configuration from the registry built
// there, GET /settings describes the service from it, and a test holds every
// os.Getenv in the repo to it. The entity-type registry below is static and
// host-agnostic: it lets agents self-discover where to look up entity refs they
// find in card_links / entity_tags. kanban-store does NOT proxy to these
// services (dumb store) — it just publishes the map.
package config

import "github.com/kayushkin/kanban-store/internal/model"

// UUIDPattern matches the canonical 8-4-4-4-12 hex form, the id shape
// noteboard hands out. Resolvers anchor it and match case-insensitively.
const UUIDPattern = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`

// EntityTypes is the canonical registry. Add new types here as needed.
// Empty Service/Search means kanban knows about the type but there is no
// upstream service to autocomplete against (e.g. git_repo URLs).
//
// Get + IDPatterns power reference resolution (dash's POST /api/resolve): a
// resolver takes an id found in prose, collects every type whose pattern
// matches it, and probes each type's Get route to learn what the id actually
// names. Adding a resolvable type is therefore one row here — no resolver or
// frontend change. Rows without Get (skill and tool ids are small integers,
// email refs are compound strings) stay search-only.
var EntityTypes = []model.EntityTypeInfo{
	{
		Type: "session", Service: "llm-bridge-server", Search: "/api/sessions?q=",
		Get: "/sessions/{id}",
		// Bridge session ids are self-identifying: a snowflake with a br_
		// prefix, or a herald-/autoworker- name ending in one. Harness session
		// uuids are NOT listed — llm-bridge-server cannot fetch by them.
		IDPatterns: []string{
			`br_\d{16,19}`,
			`(?:herald|autoworker)(?:-[a-z0-9]+)*-\d{16,19}`,
		},
	},
	{Type: "instance", Service: "llm-bridge-server", Search: "/api/instances?q="},
	// A machine is harness-store's row (m_localhost, m_dab03bcd1cb57fe7), which
	// llm-bridge-server serves at /machines. This row used to name healthcheck's
	// /api/services, a route that answers 404 and has never listed a machine.
	{Type: "machine", Service: "llm-bridge-server", Search: "/machines"},
	{Type: "service", Service: "healthcheck", Search: "/api/services?q="},
	{Type: "skill", Service: "skill-store", Search: "/skills?q="},
	{Type: "tool", Service: "tool-store", Search: "/tools?q="},
	{
		// A card link's repo ref is a local filesystem path. A chat message
		// names a repo as repo:<name> instead (a bare name is an ordinary
		// word), and only that form matches the pattern, so only it is sent to
		// repo-store's by-mention route.
		Type: "repo", Service: "repo-store",
		Get:        "/repos/by-mention/{id}",
		IDPatterns: []string{`repo:[\w.-]+`},
	},
	{Type: "git_repo"}, // remote URL; no upstream
	{
		// <repo>@<sha>, where the repo is repo-store's name (dash@8487d32) or
		// the GitHub owner/name a card link writes (kayushkin/dash@8487d32).
		// work-graph-store reads the commit from the repo's clone. The sha is
		// at least seven hex characters, as git abbreviates it.
		Type: "commit", Service: "work-graph-store",
		Get:        "/commits/{id}",
		IDPatterns: []string{`(?:[\w.-]+/)?[\w.-]+@[0-9a-f]{7,40}`},
	},
	{Type: "agent", Service: "agent-store", Search: "/api/agents?q="},
	{
		Type: "note", Service: "noteboard", Search: "/api/items?q=",
		Get: "/api/items/{id}",
		// One id space for every noteboard item type (note, todo, rank,
		// workspace); the fetched item's own `type` field is the authority on
		// which it is.
		IDPatterns: []string{UUIDPattern},
	},

	{
		Type: "prediction", Service: "prediction-store", Search: "/predictions?q=",
		Get: "/predictions/{id}",
		// Self-identifying by design: prediction-store mints prediction_000042
		// rather than a uuid precisely so this pattern can exist without
		// colliding with note's uuid claim above — a bare-uuid pattern here
		// would make every uuid in every chat message probe that store too.
		IDPatterns: []string{`prediction_\d{6,}`},
	},

	{
		Type: "article", Service: "article-store", Search: "/articles?q=",
		Get: "/articles/{id}",
		// Same design as prediction: article-store mints article_000001, so
		// the pattern claims no uuid and no bare number.
		IDPatterns: []string{`article_\d{6,}`},
	},

	{
		Type: "project", Service: "project-store", Search: "/projects?q=",
		Get: "/projects/{id}",
		// Same design as prediction: project-store mints project_000001, so the
		// pattern claims no uuid and no bare number. A card links a project
		// with entity_type "project", and GET /api/entities/project/{id}/cards
		// is the project's rollup of cards with their shared work_state.
		IDPatterns: []string{`project_\d{6,}`},
	},

	{
		Type: "principal", Service: "principal-store", Search: "/principals?q=",
		Get: "/principals/{id}",
		// Same design as prediction: principal-store mints principal_000001
		// rather than a uuid, so this pattern can exist without colliding with
		// note's uuid claim. The pattern is also what the card-assignment write
		// path checks an id against before calling principal-store — declared
		// once, in PrincipalIDPattern, and read from both places.
		IDPatterns: []string{PrincipalIDPattern},
	},

	{
		Type: "person", Service: "people-store", Search: "/persons?q=",
		Get: "/persons/{id}",
		// A person in the operator's own life (friend, family, coworker), as
		// people-store (127.0.0.1:8321) holds them for a journal and messages;
		// not a principal, which is someone who logs in or is assigned work.
		// Same design as prediction: people-store mints person_000001, so the
		// pattern claims no uuid and no bare number. A deleted person still
		// answers Get, so a link to them keeps resolving.
		IDPatterns: []string{`person_\d{6,}`},
	},

	{
		Type: "entry", Service: "journal-store", Search: "/entries?q=",
		Get: "/entries/{id}",
		// A piece of the operator's own writing — a journal entry, post or
		// note — as journal-store (127.0.0.1:8322) holds it. Same design as
		// prediction: journal-store mints entry_000001, so the pattern claims
		// no uuid and no bare number. A deleted entry answers Get with 404, an
		// honest miss.
		IDPatterns: []string{`entry_\d{6,}`},
	},

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
