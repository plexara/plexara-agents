package mcp

import (
	"sort"
	"strings"
)

// Catalog aggregates the tool catalog across every connected server.
//
// Tools is the flat list, ordered first by server name then by bare
// tool name. ToolsByServer is the same set indexed by server name.
type Catalog struct {
	Tools         []Tool
	ToolsByServer map[string][]Tool
}

func (c *Catalog) copy() *Catalog {
	if c == nil {
		return &Catalog{}
	}
	out := &Catalog{
		Tools:         make([]Tool, len(c.Tools)),
		ToolsByServer: make(map[string][]Tool, len(c.ToolsByServer)),
	}
	copy(out.Tools, c.Tools)
	for k, v := range c.ToolsByServer {
		dup := make([]Tool, len(v))
		copy(dup, v)
		out.ToolsByServer[k] = dup
	}
	return out
}

// Toolkit is a logical grouping of related tools — typically tools
// from the same MCP "namespace" (e.g. all `datahub_*` tools on a
// Plexara server).
type Toolkit struct {
	Name  string
	Tools []Tool
}

// ToolkitClassifier maps a tool to a toolkit name. Returning the
// empty string places the tool in the "default" toolkit.
type ToolkitClassifier func(t Tool) string

// PrefixClassifier classifies tools by the substring before the first
// underscore in the bare tool name. For Plexara's catalog, this groups
// `datahub_*`, `trino_*`, `s3_*`, and `memory_*` cleanly.
//
// Names with no underscore, an empty bare name, or a leading underscore
// (`_foo`) all fall into the "misc" toolkit.
func PrefixClassifier(t Tool) string {
	if t.BareName == "" {
		return "misc"
	}
	idx := strings.Index(t.BareName, "_")
	if idx <= 0 {
		return "misc"
	}
	return t.BareName[:idx]
}

// Toolkits groups the catalog using [PrefixClassifier].
func (c *Catalog) Toolkits() []Toolkit {
	return c.ToolkitsBy(PrefixClassifier)
}

// ToolkitsBy groups the catalog using a custom classifier. Toolkits
// are returned in stable alphabetical order by name; tools within a
// toolkit are returned in the order they appear in [Catalog.Tools].
func (c *Catalog) ToolkitsBy(fn ToolkitClassifier) []Toolkit {
	if c == nil || len(c.Tools) == 0 {
		return nil
	}
	groups := make(map[string][]Tool)
	for _, t := range c.Tools {
		name := fn(t)
		if name == "" {
			name = "default"
		}
		groups[name] = append(groups[name], t)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Toolkit, 0, len(names))
	for _, name := range names {
		out = append(out, Toolkit{Name: name, Tools: groups[name]})
	}
	return out
}
