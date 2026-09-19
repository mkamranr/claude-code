package translate

import (
	"encoding/json"
	"testing"
)

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

func TestSystemBecomesLeadingSystemMessage(t *testing.T) {
	in := mustJSON(t, `{"system":"be terse","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, err := ToOpenAI(in, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be terse" {
		t.Errorf("first message = %v, want system/be terse", first)
	}
	if out["model"] != "fallback" {
		t.Errorf("model = %v, want fallback when request omits it", out["model"])
	}
}

func TestAssistantToolUseBecomesToolCalls(t *testing.T) {
	in := mustJSON(t, `{"messages":[
		{"role":"assistant","content":[
			{"type":"text","text":"calling"},
			{"type":"tool_use","id":"tu_1","name":"read","input":{"path":"a.txt"}}]}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	msg := out["messages"].([]any)[0].(map[string]any)
	calls := msg["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("got %d tool_calls, want 1", len(calls))
	}
	c := calls[0].(map[string]any)
	fn := c["function"].(map[string]any)
	if c["id"] != "tu_1" || fn["name"] != "read" {
		t.Errorf("tool call = %v", c)
	}
	if fn["arguments"] != `{"path":"a.txt"}` {
		t.Errorf("arguments = %v, want serialized JSON string", fn["arguments"])
	}
}

// Anthropic puts tool results in a user message; OpenAI needs role:tool messages.
func TestToolResultBecomesSeparateToolMessage(t *testing.T) {
	in := mustJSON(t, `{"messages":[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu_1","content":"file body"}]}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1 tool message only", len(msgs))
	}
	m := msgs[0].(map[string]any)
	if m["role"] != "tool" || m["tool_call_id"] != "tu_1" || m["content"] != "file body" {
		t.Errorf("tool message = %v", m)
	}
}

func TestToolsBecomeFunctions(t *testing.T) {
	in := mustJSON(t, `{"tools":[
		{"name":"read","description":"reads","input_schema":{"type":"object"}}]}`)
	out, err := ToOpenAI(in, "m")
	if err != nil {
		t.Fatal(err)
	}
	tl := out["tools"].([]any)[0].(map[string]any)
	fn := tl["function"].(map[string]any)
	if tl["type"] != "function" || fn["name"] != "read" {
		t.Errorf("tool = %v", tl)
	}
	if fn["parameters"] == nil {
		t.Error("input_schema did not become parameters")
	}
}

func TestToolChoiceMapping(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{`{"type":"auto"}`, "auto"},
		{`{"type":"any"}`, "required"},
		{`{"type":"none"}`, "none"},
	}
	for _, c := range cases {
		req := mustJSON(t, `{"tool_choice":`+c.in+`}`)
		out, err := ToOpenAI(req, "m")
		if err != nil {
			t.Fatal(err)
		}
		if out["tool_choice"] != c.want {
			t.Errorf("tool_choice %s -> %v, want %v", c.in, out["tool_choice"], c.want)
		}
	}
	req := mustJSON(t, `{"tool_choice":{"type":"tool","name":"read"}}`)
	out, _ := ToOpenAI(req, "m")
	tc := out["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("named tool_choice = %v", tc)
	}
}

func TestStopSequencesBecomeStop(t *testing.T) {
	in := mustJSON(t, `{"stop_sequences":["END"],"max_tokens":50,"temperature":0.3,"stream":true}`)
	out, _ := ToOpenAI(in, "m")
	if out["stop"] == nil {
		t.Error("stop_sequences did not map to stop")
	}
	if out["max_tokens"] != float64(50) || out["temperature"] != 0.3 || out["stream"] != true {
		t.Errorf("passthrough params mangled: %v", out)
	}
}

func TestMapStopReason(t *testing.T) {
	cases := map[string]string{
		"stop": "end_turn", "length": "max_tokens",
		"tool_calls": "tool_use", "content_filter": "end_turn", "": "end_turn",
	}
	for in, want := range cases {
		if got := MapStopReason(in); got != want {
			t.Errorf("MapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}
