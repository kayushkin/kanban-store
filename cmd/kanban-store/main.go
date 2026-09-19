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
	"github.com/kayushkin/kanban-store/internal/filestore"
	"github.com/kayushkin/kanban-store/internal/grantstore"
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

	// Attachments live in file-store. Without it this store has none, and the
	// attachment routes answer 503 rather than report that a card has no files.
	if fileStoreURL := config.FileStoreURL(); fileStoreURL != "" {
		fileStoreToken := config.FileStoreServiceToken()
		if fileStoreToken == "" {
			log.Fatal("FILE_STORE_URL is set but FILE_STORE_SERVICE_TOKEN is not: file-store takes no call without it")
		}
		a.SetFileStore(filestore.New(fileStoreURL, fileStoreToken))
		log.Printf("attachments: files are kept in file-store at %s", fileStoreURL)
	} else {
		log.Printf("attachments: FILE_STORE_URL is not set; the attachment routes answer 503")
	}

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

	enforcement, err := config.ReadPrincipalEnforcementSettings()
	if err != nil {
		log.Fatal(err)
	}
	a.SetPrincipalEnforcement(api.PrincipalEnforcement{
		ServiceToken: enforcement.ServiceToken,
		Grants:       grantstore.New(enforcement.GrantStoreURL, enforcement.GrantStoreServiceToken),
	})
	log.Printf("every request needs X-Principal-Id (set by the gateway from a login) or the service token; board grants come from grant-store at %s, and an administrator is past every check", enforcement.GrantStoreURL)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("kanban-store listening on %s (db: %s, noteboard: %s, principal-store: %s, llm-bridge-server: %s, bundle-store: %s)",
		addr, dbPath, noteboardURL, principalStoreURL, llmBridgeServerURL, bundleStoreURL)
	log.Fatal(http.ListenAndServe(addr, a.Handler()))
}
