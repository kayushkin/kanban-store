package config

import "os"

// DefaultAuthStoreURL is where auth-store listens on this host when
// AUTH_STORE_URL is unset — the name every other service here reads.
const DefaultAuthStoreURL = "http://127.0.0.1:8303"

// MultichatURL is multichat's base URL (e.g. http://localhost:8402), read from
// MULTICHAT_URL. Empty is a real answer rather than a gap to fill: message
// triggers still match and render, and every delivery is recorded with status
// not_configured, so a trigger set up before the wiring is visibly firing into
// nothing instead of silently doing nothing.
func MultichatURL() string { return os.Getenv("MULTICHAT_URL") }

// AuthStoreURL is where multichat's API token is resolved from (provider
// "multichat"), read from AUTH_STORE_URL.
func AuthStoreURL() string {
	if v := os.Getenv("AUTH_STORE_URL"); v != "" {
		return v
	}
	return DefaultAuthStoreURL
}

// AuthStoreToken is auth-store's own bearer, read from AUTH_STORE_TOKEN.
func AuthStoreToken() string { return os.Getenv("AUTH_STORE_TOKEN") }
