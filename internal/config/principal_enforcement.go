package config

import (
	"fmt"
	"os"
)

// PrincipalEnforcementSettings is what main needs to turn on principal
// enforcement (internal/api/principal_access.go).
type PrincipalEnforcementSettings struct {
	Enabled       bool
	ServiceToken  string
	GrantStoreURL string
	// GrantStoreServiceToken is sent to grant-store; empty is right only for a
	// grant-store that does not enforce principals.
	GrantStoreServiceToken string
}

// PrincipalEnforcementRequiredValue is the one value that turns enforcement on.
const PrincipalEnforcementRequiredValue = "required"

// ReadPrincipalEnforcementSettings reads KANBAN_STORE_PRINCIPAL_ENFORCEMENT,
// KANBAN_STORE_SERVICE_TOKEN and GRANT_STORE_URL. Unset enforcement is off. Any
// other value than "required" is an error, and so is "required" without a
// service token or a grant-store URL: none of them has a default, because a
// guessed one would start a store that believes it is enforcing and is not.
func ReadPrincipalEnforcementSettings() (PrincipalEnforcementSettings, error) {
	switch value := os.Getenv("KANBAN_STORE_PRINCIPAL_ENFORCEMENT"); value {
	case "":
		return PrincipalEnforcementSettings{}, nil
	case PrincipalEnforcementRequiredValue:
	default:
		return PrincipalEnforcementSettings{}, fmt.Errorf("KANBAN_STORE_PRINCIPAL_ENFORCEMENT=%q: leave it unset for no enforcement or set it to %q", value, PrincipalEnforcementRequiredValue)
	}
	settings := PrincipalEnforcementSettings{
		Enabled:                true,
		ServiceToken:           os.Getenv("KANBAN_STORE_SERVICE_TOKEN"),
		GrantStoreURL:          os.Getenv("GRANT_STORE_URL"),
		GrantStoreServiceToken: os.Getenv("GRANT_STORE_SERVICE_TOKEN"),
	}
	if len(settings.ServiceToken) < 32 {
		return settings, fmt.Errorf("KANBAN_STORE_PRINCIPAL_ENFORCEMENT=required needs KANBAN_STORE_SERVICE_TOKEN of at least 32 characters")
	}
	if settings.GrantStoreURL == "" {
		return settings, fmt.Errorf("KANBAN_STORE_PRINCIPAL_ENFORCEMENT=required needs GRANT_STORE_URL")
	}
	return settings, nil
}
