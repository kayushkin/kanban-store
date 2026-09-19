package config

import "os"

// FileStoreURL is file-store's base URL (http://127.0.0.1:8317), read from
// FILE_STORE_URL. Empty is a real answer: this store then has no attachments,
// and every attachment route says so with a 503 rather than pretending a card
// has none.
func FileStoreURL() string { return os.Getenv("FILE_STORE_URL") }

// FileStoreServiceToken is what file-store asks of every caller, read from
// FILE_STORE_SERVICE_TOKEN. It reads every file, so it lives in
// ~/.config/file-store-tokens.env and reaches this unit through a host-local
// drop-in — never the tracked unit, and never the shared principal-gating
// file, whose variables every agent session inherits.
func FileStoreServiceToken() string { return os.Getenv("FILE_STORE_SERVICE_TOKEN") }
