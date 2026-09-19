// Package filter removes request shapes that strict, non-Anthropic endpoints
// reject. Each rule corresponds to an observed HTTP 400 or render failure
// against third-party Anthropic-compatible endpoints; see the design doc.
//
// All functions are pure: no network, no clock, no filesystem.
package filter

import "fmt"

// allowedBlockTypes is a whitelist. Anything else is dropped, because a single
// unrecognized content block type fails the entire request on strict servers.
var allowedBlockTypes = map[string]bool{
	"text":                       true,
	"image":                      true,
	"tool_use":                   true,
	"tool_result":                true,
	"thinking":                   true,
	"redacted_thinking":          true,
	"document":                   true,
	"server_tool_use":            true,
	"web_search_tool_result":     true,
	"web_fetch_tool_result":      true,
	"mcp_tool_use":               true,
	"mcp_tool_result":            true,
	"code_execution_tool_result": true,
}

// schemaKeywordsToStrip are JSON Schema keywords that strict validators on the
// server side have rejected outright rather than ignoring.
var schemaKeywordsToStrip = []string{"pattern", "regex"}

// Request returns a filtered copy of an Anthropic Messages request along with
// notes describing anything removed. The input is not mutated.
func Request(req map[string]any) (map[string]any, []string) {
	var notes []string
	out := cloneMap(req)

	if sys, ok := out["system"]; ok {
		out["system"] = filterContent(sys, &notes, "system")
	}

	if msgs, ok := out["messages"].([]any); ok {
		filtered := make([]any, 0, len(msgs))
		for i, raw := range msgs {
			msg, ok := raw.(map[string]any)
			if !ok {
				filtered = append(filtered, raw)
				continue
			}
			m := cloneMap(msg)
			delete(m, "cache_control")
			if c, ok := m["content"]; ok {
				m["content"] = filterContent(c, &notes, fmt.Sprintf("messages[%d]", i))
			}
			filtered = append(filtered, m)
		}
		out["messages"] = filtered
	}

	if tools, ok := out["tools"].([]any); ok {
		filtered := make([]any, 0, len(tools))
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				filtered = append(filtered, raw)
				continue
			}
			tl := cloneMap(tool)
			delete(tl, "cache_control")
			if s, ok := tl["input_schema"]; ok {
				tl["input_schema"] = StripSchemaKeywords(s)
			}
			filtered = append(filtered, tl)
		}
		out["tools"] = filtered
	}

	return out, notes
}

// filterContent handles both the string and block-array forms of content.
func filterContent(content any, notes *[]string, where string) any {
	blocks, ok := content.([]any)
	if !ok {
		return content // plain string form needs no filtering
	}
	kept := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		typ, _ := block["type"].(string)
		if !allowedBlockTypes[typ] {
			*notes = append(*notes, fmt.Sprintf("dropped %s content block of type %q", where, typ))
			continue
		}
		b := cloneMap(block)
		delete(b, "cache_control")
		kept = append(kept, b)
	}
	return kept
}

// StripSchemaKeywords recursively removes validator-hostile keywords from a
// JSON Schema while leaving its structure intact.
func StripSchemaKeywords(schema any) any {
	return stripSchemaNode(schema)
}

// stripSchemaNode walks a schema node, stripping keyword keys but preserving
// property names within properties/definitions/patternProperties objects.
func stripSchemaNode(v any) any {
	switch s := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(s))
		for k, val := range s {
			if isStrippedKeyword(k) {
				continue
			}
			switch k {
			case "properties", "$defs", "definitions", "patternProperties":
				out[k] = stripSchemaDict(val)
			default:
				out[k] = stripSchemaNode(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(s))
		for i, e := range s {
			out[i] = stripSchemaNode(e)
		}
		return out
	default:
		return v
	}
}

// stripSchemaDict walks a map whose keys are property names, not keywords.
func stripSchemaDict(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return stripSchemaNode(v)
	}
	out := make(map[string]any, len(m))
	for name, sub := range m {
		out[name] = stripSchemaNode(sub)
	}
	return out
}

func isStrippedKeyword(k string) bool {
	for _, s := range schemaKeywordsToStrip {
		if k == s {
			return true
		}
	}
	return false
}

// EnsureToolUseIDs guarantees every tool_use block carries an id. A tool_use
// streamed without one crashes the Claude Code renderer on the first tool call.
func EnsureToolUseIDs(content []any, gen func() string) []any {
	out := make([]any, len(content))
	for i, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		if t, _ := block["type"].(string); t == "tool_use" {
			if id, _ := block["id"].(string); id == "" {
				b := cloneMap(block)
				b["id"] = gen()
				out[i] = b
				continue
			}
		}
		out[i] = block
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
