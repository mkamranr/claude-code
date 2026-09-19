package translate

import (
	"encoding/json"
	"testing"
)

func decodeResp(t *testing.T, s string) OpenAIResponse {
	t.Helper()
	var r OpenAIResponse
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return r
}

func TestTextResponseShape(t *testing.T) {
	r := decodeResp(t, `{"id":"c1","model":"m","choices":[
		{"message":{"content":"hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	got := FromOpenAI(r, "fallback", func() string { return "gen" })

	if got["type"] != "message" || got["role"] != "assistant" {
		t.Errorf("envelope = %v", got)
	}
	content := got["content"].([]any)
	block := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "hello" {
		t.Errorf("content block = %v", block)
	}
	if got["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", got["stop_reason"])
	}
	usage := got["usage"].(map[string]any)
	if usage["input_tokens"] != 7 || usage["output_tokens"] != 3 {
		t.Errorf("usage = %v", usage)
	}
}

func TestToolCallBecomesToolUseBlockWithParsedInput(t *testing.T) {
	r := decodeResp(t, `{"id":"c1","model":"m","choices":[
		{"message":{"content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},
		 "finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })

	content := got["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("got %d blocks, want 1 (empty text must not emit a block)", len(content))
	}
	b := content[0].(map[string]any)
	if b["type"] != "tool_use" || b["id"] != "call_1" || b["name"] != "read" {
		t.Errorf("tool_use block = %v", b)
	}
	input := b["input"].(map[string]any)
	if input["path"] != "a.txt" {
		t.Errorf("arguments were not parsed into input: %v", input)
	}
	if got["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", got["stop_reason"])
	}
}

// A tool_use block without an id crashes the Claude Code renderer.
func TestMissingToolCallIDIsGenerated(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"type":"function","function":{"name":"x","arguments":"{}"}}]},
		"finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "toolu_generated" })
	b := got["content"].([]any)[0].(map[string]any)
	if b["id"] != "toolu_generated" {
		t.Errorf("id = %v, want generated", b["id"])
	}
}

func TestUnparseableArgumentsDoNotPanic(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"id":"c","type":"function","function":{"name":"x","arguments":"not json"}}]},
		"finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })
	b := got["content"].([]any)[0].(map[string]any)
	if _, ok := b["input"].(map[string]any); !ok {
		t.Errorf("input should fall back to an empty object, got %v", b["input"])
	}
}

func TestEmptyChoicesYieldsEmptyContent(t *testing.T) {
	got := FromOpenAI(OpenAIResponse{}, "m", func() string { return "gen" })
	if len(got["content"].([]any)) != 0 {
		t.Errorf("content = %v, want empty", got["content"])
	}
	if got["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", got["stop_reason"])
	}
}

func TestNullArgumentsYieldEmptyObjectNotNull(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"id":"c","type":"function","function":{"name":"x","arguments":"null"}}]},
		"finish_reason":"tool_calls"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })
	b := got["content"].([]any)[0].(map[string]any)
	input, ok := b["input"].(map[string]any)
	if !ok || input == nil {
		t.Fatalf("input = %#v, want a non-nil empty object", b["input"])
	}
	// It must also serialize as {} rather than null.
	enc, _ := json.Marshal(b["input"])
	if string(enc) != "{}" {
		t.Errorf("input serialized as %s, want {}", enc)
	}
}

func TestStopReasonUpgradedWhenServerSaysStopButEmittedToolCalls(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"id":"c","type":"function","function":{"name":"x","arguments":"{}"}}]},
		"finish_reason":"stop"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })
	if got["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", got["stop_reason"])
	}
}

func TestTruncationIsNotMaskedByTheToolUseUpgrade(t *testing.T) {
	r := decodeResp(t, `{"choices":[{"message":{"tool_calls":[
		{"id":"c","type":"function","function":{"name":"x","arguments":"{}"}}]},
		"finish_reason":"length"}]}`)
	got := FromOpenAI(r, "m", func() string { return "gen" })
	if got["stop_reason"] != "max_tokens" {
		t.Errorf("stop_reason = %v, want max_tokens — a truncated response must not be reported as a clean tool call", got["stop_reason"])
	}
}
