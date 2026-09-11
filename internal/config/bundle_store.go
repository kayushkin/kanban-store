package config

import "os"

// DefaultBundleStoreURL is where bundle-store listens on this host when
// BUNDLE_STORE_URL is unset — the name dash already reads.
const DefaultBundleStoreURL = "http://127.0.0.1:8307"

// BundleStoreURL is the base URL kanban-store checks a board's default bundle
// against before writing it, read from BUNDLE_STORE_URL.
func BundleStoreURL() string {
	if v := os.Getenv("BUNDLE_STORE_URL"); v != "" {
		return v
	}
	return DefaultBundleStoreURL
}
