package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kayushkin/kanban-store/internal/api"
	"github.com/kayushkin/kanban-store/internal/db"
	"github.com/kayushkin/kanban-store/internal/noteboard"
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

	nb := noteboard.New(noteboardURL)
	a := api.New(store, nb)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("kanban-store listening on %s (db: %s, noteboard: %s)", addr, dbPath, noteboardURL)
	log.Fatal(http.ListenAndServe(addr, a.Handler()))
}
