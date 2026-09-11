package config

import "os"

// DefaultLLMBridgeServerURL is where llm-bridge-server listens on this host
// when LLM_BRIDGE_URL is unset. The shipped systemd unit sets the variable
// explicitly; the default exists so a bare `make run` on the same host works.
const DefaultLLMBridgeServerURL = "http://127.0.0.1:8160"

// LLMBridgeServerURL is the base URL kanban-store checks a board's default
// agent and default instance against before writing either, read from
// LLM_BRIDGE_URL — the name grant-store, llm-bridge-adapter and dash already
// read, so one host has one name for the owner.
func LLMBridgeServerURL() string {
	if v := os.Getenv("LLM_BRIDGE_URL"); v != "" {
		return v
	}
	return DefaultLLMBridgeServerURL
}
