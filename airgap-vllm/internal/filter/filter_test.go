package filter

import (
	"encoding/json"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad test fixture: %v", err)
	}
	return m
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// Reproduces the shipped regression: 400 "Input tag 'advisor_20260301'".
func TestDropsUnknownContentBlockTypes(t *testing.T) {
	in := decode(t, `{"messages":[{"role":"user","content":[
		{"type":"text","text":"hi"},
		{"type":"advisor_20260301","payload":"x"}
	]}]}`)
	out, notes := Request(in)
	got := encode(t, out)
	if strings.Contains(got, "advisor_20260301") {
		t.Errorf("unknown block survived: %s", got)
	}
	if !strings.Contains(got, `"hi"`) {
		t.Errorf("known block was dropped: %s", got)
	}
	if len(notes) == 0 {
		t.Error("dropping a block should produce a note")
	}
}

func TestDropsToolReferenceBlocks(t *testing.T) {
	in := decode(t, `{"messages":[{"role":"user","content":[
		{"type":"tool_reference","name":"x"},
		{"type":"text","text":"keep"}
	]}]}`)
	out, _ := Request(in)
	if strings.Contains(encode(t, out), "tool_reference") {
		t.Error("tool_reference block survived")
	}
}

func TestStripsCacheControlEverywhere(t *testing.T) {
	in := decode(t, `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"u","cache_control":{"type":"ephemeral"}}]}],
		"tools":[{"name":"t","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]}`)
	out, _ := Request(in)
	if strings.Contains(encode(t, out), "cache_control") {
		t.Errorf("cache_control survived: %s", encode(t, out))
	}
}

func TestStripsSchemaPatternKeywords(t *testing.T) {
	in := decode(t, `{"tools":[{"name":"t","input_schema":{
		"type":"object",
		"properties":{
			"a":{"type":"string","pattern":"^[a-z]+$"},
			"b":{"type":"object","properties":{"c":{"type":"string","pattern":"x"}}}
		}}}]}`)
	out, _ := Request(in)
	got := encode(t, out)
	if strings.Contains(got, "pattern") {
		t.Errorf("pattern keyword survived at some depth: %s", got)
	}
	if !strings.Contains(got, `"type":"string"`) {
		t.Errorf("stripping removed too much: %s", got)
	}
}

func TestPreservesUnrelatedFields(t *testing.T) {
	in := decode(t, `{"model":"m","max_tokens":100,"stream":true,
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, notes := Request(in)
	if out["model"] != "m" || out["stream"] != true {
		t.Errorf("top-level fields mangled: %s", encode(t, out))
	}
	if len(notes) != 0 {
		t.Errorf("clean request produced notes: %v", notes)
	}
}

func TestEnsureToolUseIDsFillsMissingID(t *testing.T) {
	content := []any{
		map[string]any{"type": "tool_use", "name": "x", "input": map[string]any{}},
		map[string]any{"type": "tool_use", "id": "keep_me", "name": "y", "input": map[string]any{}},
	}
	n := 0
	out := EnsureToolUseIDs(content, func() string { n++; return "gen_1" })
	first := out[0].(map[string]any)
	second := out[1].(map[string]any)
	if first["id"] != "gen_1" {
		t.Errorf("missing id not filled, got %v", first["id"])
	}
	if second["id"] != "keep_me" {
		t.Errorf("existing id overwritten, got %v", second["id"])
	}
	if n != 1 {
		t.Errorf("generator called %d times, want 1", n)
	}
}

func TestKeepsAPropertyLiterallyNamedPattern(t *testing.T) {
	in := decode(t, `{"tools":[{"name":"Grep","input_schema":{
		"type":"object",
		"properties":{
			"pattern":{"type":"string","description":"regex to search for","pattern":"^.+$"},
			"path":{"type":"string"}
		},
		"required":["pattern"]}}]}`)
	out, _ := Request(in)
	got := encode(t, out)

	if !strings.Contains(got, `"pattern":{`) {
		t.Errorf("property named pattern was deleted: %s", got)
	}
	if !strings.Contains(got, `"regex to search for"`) {
		t.Errorf("property description lost: %s", got)
	}
	if strings.Contains(got, `"^.+$"`) {
		t.Errorf("pattern keyword inside the property schema survived: %s", got)
	}
	if !strings.Contains(got, `"required":["pattern"]`) {
		t.Errorf("required list should be untouched: %s", got)
	}
}

func TestAllowsKnownToolBlockTypes(t *testing.T) {
	in := decode(t, `{"messages":[{"role":"user","content":[
		{"type":"mcp_tool_use","name":"x","input":{}},
		{"type":"advisor_20260301","payload":"x"}
	]}]}`)
	out, _ := Request(in)
	got := encode(t, out)
	if !strings.Contains(got, "mcp_tool_use") {
		t.Errorf("mcp_tool_use block was dropped: %s", got)
	}
	if strings.Contains(got, "advisor_20260301") {
		t.Errorf("unknown block advisor_20260301 survived: %s", got)
	}
}
