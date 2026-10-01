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
		if et.Search != "" && et.Get != "" && len(et.IDPatterns) > 0 && et.LabelField == "" {
			t.Errorf("type %q: has Search, Get and IDPatterns but no LabelField — a client offering its records as mentions could not name them", et.Type)
		}
		if et.SearchResults != "" && et.Search == "" {
			t.Errorf("type %q: has SearchResults but no Search route", et.Type)
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
		{"article_000001", map[string]bool{"article": true, "prediction": false, "note": false}},
		{"article_12", map[string]bool{"article": false}}, // fewer than six digits
		{"project_000001", map[string]bool{"project": true, "prediction": false, "principal": false, "note": false}},
		{"project_12", map[string]bool{"project": false}},      // fewer than six digits
		{"principal_123", map[string]bool{"principal": false}}, // fewer than six digits
		{"person_000001", map[string]bool{"person": true, "principal": false, "prediction": false, "note": false}},
		{"principal_000001", map[string]bool{"person": false}},
		{"person_12", map[string]bool{"person": false}}, // fewer than six digits
		{"entry_000001", map[string]bool{"entry": true, "person": false, "principal": false, "note": false}},
		{"entry_12", map[string]bool{"entry": false}}, // fewer than six digits
		{"not-an-id", map[string]bool{"session": false, "note": false}},
		{"repo:dash", map[string]bool{"repo": true, "commit": false}},
		{"repo:kayushkin.com", map[string]bool{"repo": true}},
		{"dash", map[string]bool{"repo": false}}, // a bare name is prose
		{"dash@8487d32", map[string]bool{"commit": true, "repo": false}},
		{"kayushkin/dash@8487d32f0e5a0c4d1b3e2f6a7c8d9e0f1a2b3c4d", map[string]bool{"commit": true}},
		{"dash@8487d3", map[string]bool{"commit": false}},  // shorter than git abbreviates
		{"react@19.0.0", map[string]bool{"commit": false}}, // a package version
	}
	for _, c := range cases {
		for typ, want := range c.wantType {
			if got := match(byType[typ], c.id); got != want {
				t.Errorf("id %q against type %q: got %v, want %v", c.id, typ, got, want)
			}
		}
	}
}
