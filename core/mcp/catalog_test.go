package mcp_test

import (
	"testing"

	"github.com/plexara/plexara-agents/core/mcp"
)

func TestCatalog_PrefixToolkits(t *testing.T) {
	t.Parallel()

	cat := &mcp.Catalog{
		Tools: []mcp.Tool{
			{Name: "p__datahub_search", Server: "p", BareName: "datahub_search"},
			{Name: "p__datahub_get_schema", Server: "p", BareName: "datahub_get_schema"},
			{Name: "p__trino_query", Server: "p", BareName: "trino_query"},
			{Name: "p__memory_recall", Server: "p", BareName: "memory_recall"},
			{Name: "fs__read", Server: "fs", BareName: "read"},
		},
	}

	got := cat.Toolkits()
	wantNames := []string{"datahub", "memory", "misc", "trino"}
	if len(got) != len(wantNames) {
		t.Fatalf("len(toolkits) = %d; want %d (%v)", len(got), len(wantNames), names(got))
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("toolkit[%d].Name = %q; want %q", i, got[i].Name, want)
		}
	}
	if got[0].Name == "datahub" && len(got[0].Tools) != 2 {
		t.Errorf("datahub toolkit has %d tools; want 2", len(got[0].Tools))
	}
}

func TestCatalog_CustomClassifier(t *testing.T) {
	t.Parallel()

	cat := &mcp.Catalog{
		Tools: []mcp.Tool{
			{Server: "a", BareName: "x"},
			{Server: "b", BareName: "y"},
		},
	}
	got := cat.ToolkitsBy(func(t mcp.Tool) string { return t.Server })
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("toolkits = %v; want [a b]", names(got))
	}
}

func TestCatalog_Empty(t *testing.T) {
	t.Parallel()

	var cat *mcp.Catalog
	if got := cat.Toolkits(); got != nil {
		t.Errorf("nil Catalog.Toolkits = %v; want nil", got)
	}
	if got := (&mcp.Catalog{}).Toolkits(); got != nil {
		t.Errorf("empty Catalog.Toolkits = %v; want nil", got)
	}
}

func TestCatalog_DefaultClassifierEmptyName(t *testing.T) {
	t.Parallel()

	cat := &mcp.Catalog{Tools: []mcp.Tool{{Server: "s", BareName: "noprefix"}}}
	got := cat.ToolkitsBy(func(_ mcp.Tool) string { return "" })
	if len(got) != 1 || got[0].Name != "default" {
		t.Errorf("empty-string classifier: got %v; want a single 'default' toolkit", names(got))
	}
}

func TestCatalog_PrefixClassifierLeadingUnderscore(t *testing.T) {
	t.Parallel()

	// Per PrefixClassifier doc: leading underscore -> "misc".
	if got := mcp.PrefixClassifier(mcp.Tool{BareName: "_foo"}); got != "misc" {
		t.Errorf("PrefixClassifier(\"_foo\") = %q; want misc", got)
	}
}

func names(toolkits []mcp.Toolkit) []string {
	out := make([]string, len(toolkits))
	for i, tk := range toolkits {
		out[i] = tk.Name
	}
	return out
}
