package config

import "os"

// DefaultPrincipalStoreURL is where principal-store listens on this host when
// PRINCIPAL_STORE_URL is unset. The shipped systemd unit sets the variable
// explicitly; the default exists so a bare `make run` on the same host works.
const DefaultPrincipalStoreURL = "http://127.0.0.1:8314"

// PrincipalIDPattern is the shape of an id principal-store mints —
// principal_000001, never a bare uuid. It is declared once and read twice: by
// the entity-type registry, so reference resolvers know which ids to probe
// principal-store with, and by PUT /api/cards/{id}/assignments/{principal_id},
// which refuses an id of any other shape before it calls principal-store.
const PrincipalIDPattern = `principal_\d{6,}`

// PrincipalStoreURL is the base URL kanban-store checks principals against
// before writing a card assignment, read from PRINCIPAL_STORE_URL.
func PrincipalStoreURL() string {
	if v := os.Getenv("PRINCIPAL_STORE_URL"); v != "" {
		return v
	}
	return DefaultPrincipalStoreURL
}
