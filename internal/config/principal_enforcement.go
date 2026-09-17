package config

import (
	"fmt"
	"os"
)

// PrincipalEnforcementSettings is what every kanban-store needs before it can
// answer a request: the token internal services present, where board grants are
// read, and the token grant-store itself wants. There is no off switch — a
// store that cannot authorize a caller must not serve boards.
type PrincipalEnforcementSettings struct {
	ServiceToken  string
	GrantStoreURL string
	// GrantStoreServiceToken is sent to grant-store, which gates its own
	// routes; without it every board read is a 502.
	GrantStoreServiceToken string
}

// ReadPrincipalEnforcementSettings reads KANBAN_STORE_SERVICE_TOKEN,
// GRANT_STORE_URL and GRANT_STORE_SERVICE_TOKEN. None has a default: a guessed
// one would start a store that believes it is checking callers and is not.
func ReadPrincipalEnforcementSettings() (PrincipalEnforcementSettings, error) {
	settings := PrincipalEnforcementSettings{
		ServiceToken:           os.Getenv("KANBAN_STORE_SERVICE_TOKEN"),
		GrantStoreURL:          os.Getenv("GRANT_STORE_URL"),
		GrantStoreServiceToken: os.Getenv("GRANT_STORE_SERVICE_TOKEN"),
	}
	if len(settings.ServiceToken) < 32 {
		return settings, fmt.Errorf("KANBAN_STORE_SERVICE_TOKEN must be at least 32 characters: internal services present it, and without it every request that omits the header would be unrestricted")
	}
	if settings.GrantStoreURL == "" {
		return settings, fmt.Errorf("GRANT_STORE_URL is required: board access is read from grant-store on every request")
	}
	if len(settings.GrantStoreServiceToken) < 32 {
		return settings, fmt.Errorf("GRANT_STORE_SERVICE_TOKEN must be at least 32 characters: grant-store gates its own routes and answers a call without it 401")
	}
	return settings, nil
}
