package mcp_test

import (
	"errors"
	"testing"

	"github.com/plexara/plexara-agents/core/mcp"
)

// FuzzSplitName covers the namespaced-name parser against arbitrary
// input. The contract: SplitName never panics, never returns inputs
// that fail to round-trip through JoinName.
func FuzzSplitName(f *testing.F) {
	seeds := []string{
		"plexara__datahub_search",
		"a__b",
		"a__b__c",
		"",
		"__",
		"__tool",
		"server__",
		"no-separator",
		"a__b__",
		"___",
		"____",
		"\x00\x01__\x02",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, in string) {
		server, bare, err := mcp.SplitName(in)
		if err != nil {
			// Errors are expected; ensure they chain to the public
			// sentinel rather than carrying an ad-hoc message.
			if !errors.Is(err, mcp.ErrInvalidName) {
				t.Errorf("err = %v; want errors.Is ErrInvalidName", err)
			}
			return
		}
		// On success, both halves must be non-empty and rejoining must
		// preserve the *first* server segment exactly.
		if server == "" || bare == "" {
			t.Errorf("SplitName(%q) returned empty server=%q or bare=%q", in, server, bare)
		}
		joined := mcp.JoinName(server, bare)
		s2, b2, err := mcp.SplitName(joined)
		if err != nil {
			t.Errorf("re-Split of JoinName(%q,%q)=%q failed: %v", server, bare, joined, err)
			return
		}
		if s2 != server || b2 != bare {
			t.Errorf("round-trip diverged: %q,%q -> %q -> %q,%q", server, bare, joined, s2, b2)
		}
	})
}
