package config

import (
	"regexp"
	"strings"
	"testing"
)

// The registry is what reference resolvers (dash's POST /api/resolve) build
// on: every IDPattern must compile anchored and case-insensitive, and every
// Get route must carry the {id} placeholder. A row that violates either would
// break resolution silently, so it fails here instead.
func TestEntityTypeResolutionFieldsAreWellFormed(t *testing.T) {
	for _, et := range EntityTypes {
		if et.Get != "" && !strings.Contains(et.Get, "{id}") {
			t.Errorf("type %q: Get %q has no {id} placeholder", et.Type, et.Get)
		}
		if len(et.IDPatterns) > 0 && et.Get == "" {
			t.Errorf("type %q: has IDPatterns but no Get route — a resolver could classify the id but never fetch it", et.Type)
		}
		for _, p := range et.IDPatterns {
			if _, err := regexp.Compile(`(?i)^(?:` + p + `)$`); err != nil {
				t.Errorf("type %q: pattern %q does not compile anchored: %v", et.Type, p, err)
			}
		}
	}
}

// Pin which sample ids each resolvable type claims. These are the shapes chat
// surfaces detect in prose; a registry edit that stops matching one of them
// breaks live reference chips.
func TestEntityTypeIDPatternsClassifySampleIDs(t *testing.T) {
	match := func(patterns []string, id string) bool {
		for _, p := range patterns {
			if regexp.MustCompile(`(?i)^(?:` + p + `)$`).MatchString(id) {
				return true
			}
		}
		return false
	}
	byType := map[string][]string{}
	for _, et := range EntityTypes {
		byType[et.Type] = et.IDPatterns
	}

	cases := []struct {
		id       string
		wantType map[string]bool
	}{
		{"br_1787454393154542495", map[string]bool{"session": true, "note": false}},
		{"herald-1787454393154542495", map[string]bool{"session": true, "note": false}},
		{"herald-a-b-1787454393154542495", map[string]bool{"session": true, "note": false}},
		{"autoworker-fix-tests-1787454393154542495", map[string]bool{"session": true, "note": false}},
		{"d5e695af-8bd4-4bb2-9398-fe24774dd95f", map[string]bool{"session": false, "note": true}},
		{"D5E695AF-8BD4-4BB2-9398-FE24774DD95F", map[string]bool{"note": true}}, // case-insensitive
		{"br_123", map[string]bool{"session": false}},                           // snowflake too short
		{"principal_000001", map[string]bool{"principal": true, "prediction": false, "note": false}},
		{"prediction_000001", map[string]bool{"prediction": true, "principal": false}},
		{"principal_123", map[string]bool{"principal": false}}, // fewer than six digits
		{"not-an-id", map[string]bool{"session": false, "note": false}},
	}
	for _, c := range cases {
		for typ, want := range c.wantType {
			if got := match(byType[typ], c.id); got != want {
				t.Errorf("id %q against type %q: got %v, want %v", c.id, typ, got, want)
			}
		}
	}
}
