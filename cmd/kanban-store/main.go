package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kayushkin/kanban-store/internal/api"
	"github.com/kayushkin/kanban-store/internal/bundlestore"
	"github.com/kayushkin/kanban-store/internal/config"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/llmbridge"
	"github.com/kayushkin/kanban-store/internal/multichat"
	"github.com/kayushkin/kanban-store/internal/noteboard"
	"github.com/kayushkin/kanban-store/internal/principalstore"
)

func main() {
	port := os.Getenv("KANBAN_PORT")
	if port == "" {
		port = "8305"
	}

	dbPath := os.Getenv("KANBAN_DB")
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".kanban-store", "kanban-store.db")
	}

	noteboardURL := os.Getenv("KANBAN_NOTEBOARD_URL")
	if noteboardURL == "" {
		noteboardURL = "http://localhost:8191"
	}

	store, err := db.New(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer store.Close()

	principalStoreURL := config.PrincipalStoreURL()
	llmBridgeServerURL := config.LLMBridgeServerURL()
	bundleStoreURL := config.BundleStoreURL()
	nb := noteboard.New(noteboardURL)
	a := api.New(store, nb, principalstore.New(principalStoreURL), llmbridge.New(llmBridgeServerURL), bundlestore.New(bundleStoreURL))

	// Message triggers send through multichat only when MULTICHAT_URL is set.
	// Without it they still match and render, and each delivery is recorded as
	// not_configured, so the board's settings page shows them firing into
	// nothing rather than silently doing nothing.
	if multichatURL := config.MultichatURL(); multichatURL != "" {
		authStoreToken := config.AuthStoreToken()
		if authStoreToken == "" {
			log.Fatal("MULTICHAT_URL is set but AUTH_STORE_TOKEN is not: message triggers resolve multichat's API token from auth-store provider \"multichat\"")
		}
		a.SetMessageSender(multichat.New(multichatURL, multichat.AuthStoreTokenSource(config.AuthStoreURL(), authStoreToken)))
		log.Printf("message triggers: delivering through multichat at %s (token from auth-store %s)", multichatURL, config.AuthStoreURL())
	} else {
		log.Printf("message triggers: MULTICHAT_URL is not set; matching triggers are recorded as not_configured and nothing is sent")
	}

	addr := fmt.Sprintf(":%s", port)
	log.Printf("kanban-store listening on %s (db: %s, noteboard: %s, principal-store: %s, llm-bridge-server: %s, bundle-store: %s)",
		addr, dbPath, noteboardURL, principalStoreURL, llmBridgeServerURL, bundleStoreURL)
	log.Fatal(http.ListenAndServe(addr, a.Handler()))
}
