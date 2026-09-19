package translate

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

type recorder struct{ events []string }

func (r *recorder) Event(name string, data any) error {
	b, _ := json.Marshal(data)
	r.events = append(r.events, name+" "+string(b))
	return nil
}

func (r *recorder) names() []string {
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = strings.SplitN(e, " ", 2)[0]
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event sequence\n got %v\nwant %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("event %d = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

func chunkText(s string) Chunk {
	var c Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"content":`+strconv.Quote(s)+`}}]}`), &c)
	return c
}

func TestTextOnlyStreamSequence(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	if err := tr.Start(); err != nil {
		t.Fatal(err)
	}
	tr.Chunk(chunkText("Hel"))
	tr.Chunk(chunkText("lo"))
	tr.Finish()

	eq(t, r.names(), []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop",
		"message_delta", "message_stop",
	})
}

func TestToolCallStreamOpensSecondBlockAndClosesText(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	tr.Chunk(chunkText("thinking"))

	var first, rest, final Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`), &first)
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"arguments":"th\":\"a\"}"}}]}}]}`), &rest)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":11,"completion_tokens":4}}`), &final)

	tr.Chunk(first)
	tr.Chunk(rest)
	tr.Chunk(final)
	tr.Finish()

	eq(t, r.names(), []string{
		"message_start",
		"content_block_start", "content_block_delta", // text
		"content_block_stop", // text closed before the tool block opens
		"content_block_start", "content_block_delta", "content_block_delta",
		"content_block_stop",
		"message_delta", "message_stop",
	})

	joined := strings.Join(r.events, "\n")
	if !strings.Contains(joined, `"type":"input_json_delta"`) {
		t.Error("tool arguments were not emitted as input_json_delta")
	}
	if !strings.Contains(joined, `"id":"call_1"`) {
		t.Error("tool_use id missing from content_block_start")
	}
	if !strings.Contains(joined, `"stop_reason":"tool_use"`) {
		t.Error("stop_reason should be tool_use")
	}
	if !strings.Contains(joined, `"output_tokens":4`) {
		t.Error("final usage not carried into message_delta")
	}
}

func TestToolCallWithoutIDGetsGeneratedOne(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "toolu_gen" })
	tr.Start()
	var c Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"function":{"name":"x","arguments":"{}"}}]}}]}`), &c)
	tr.Chunk(c)
	tr.Finish()
	if !strings.Contains(strings.Join(r.events, "\n"), "toolu_gen") {
		t.Error("missing tool call id was not generated")
	}
}

// Servers sometimes report finish_reason "stop" despite emitting tool calls.
func TestStopReasonUpgradedWhenToolBlocksExist(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	var c, fin Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"c1","function":{"name":"x","arguments":"{}"}}]}}]}`), &c)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`), &fin)
	tr.Chunk(c)
	tr.Chunk(fin)
	tr.Finish()
	if !strings.Contains(strings.Join(r.events, "\n"), `"stop_reason":"tool_use"`) {
		t.Error("stop_reason should be upgraded to tool_use")
	}
}

func TestFinishIsIdempotent(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	tr.Finish()
	n := len(r.events)
	tr.Finish()
	if len(r.events) != n {
		t.Errorf("second Finish emitted %d extra events", len(r.events)-n)
	}
}

// FINDING 1: Verify multiple tool calls nest strictly — no overlapping blocks.
func TestMultipleToolCallsNestStrictly(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()

	var tool1, tool2, final Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"a\""}}]}}]}`), &tool1)
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":1,"id":"call_2","function":{"name":"write","arguments":"{\"b\""}}]}}]}`), &tool2)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`), &final)

	tr.Chunk(tool1)
	tr.Chunk(tool2)
	tr.Chunk(final)
	tr.Finish()

	names := r.names()
	// Verify strict alternation: at most one block open at a time.
	openCount := 0
	for i, name := range names {
		if name == "content_block_start" {
			openCount++
			if openCount > 1 {
				t.Fatalf("event %d: content_block_start while %d blocks already open; full: %v", i, openCount-1, names)
			}
		} else if name == "content_block_stop" {
			openCount--
			if openCount < 0 {
				t.Fatalf("event %d: content_block_stop with no open block; full: %v", i, names)
			}
		}
	}
	if openCount != 0 {
		t.Fatalf("at end, %d blocks still open; full: %v", openCount, names)
	}

	// Count: two tool blocks should produce 2 starts and 2 stops.
	starts := 0
	stops := 0
	for _, name := range names {
		if name == "content_block_start" {
			starts++
		} else if name == "content_block_stop" {
			stops++
		}
	}
	if starts != 2 || stops != 2 {
		t.Fatalf("expected 2 content_block_start and 2 content_block_stop, got %d starts and %d stops; full: %v", starts, stops, names)
	}
}

// FINDING 2a: Usage on final chunk carries input_tokens into message_delta.
func TestUsageOnFinalChunkReachesMessageDelta(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()
	tr.Chunk(chunkText("Hello"))

	var final Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":3}}`), &final)
	tr.Chunk(final)
	tr.Finish()

	joined := strings.Join(r.events, "\n")
	// message_delta should contain input_tokens from the final chunk.
	if !strings.Contains(joined, `"input_tokens":10`) {
		t.Error("input_tokens not in message_delta when usage on final chunk")
	}
}

// FINDING 2b: Usage on first chunk reaches message_start.
func TestUsageOnFirstChunkReachesMessageStart(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })

	var first Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"content":"Hi"}}],
		"usage":{"prompt_tokens":5,"completion_tokens":0}}`), &first)
	tr.Chunk(first)
	tr.Finish()

	// message_start should contain input_tokens from the first chunk.
	// Look for the pattern in message_start event (first event).
	if !strings.Contains(r.events[0], `"input_tokens":5`) {
		t.Errorf("input_tokens not in message_start when usage on first chunk; message_start = %s", r.events[0])
	}
}

// FINDING 3: Truncation (finish_reason "length") is not masked by tool_use presence.
func TestMaxTokensNotMaskedByToolPresence(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()

	var tool, truncated Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"c1","function":{"name":"x","arguments":"{}"}}]}}]}`), &tool)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"length"}]}`), &truncated)

	tr.Chunk(tool)
	tr.Chunk(truncated)
	tr.Finish()

	eventStr := strings.Join(r.events, "\n")
	if !strings.Contains(eventStr, `"stop_reason":"max_tokens"`) {
		t.Error("stop_reason should be max_tokens for length truncation")
	}
	if strings.Contains(eventStr, `"stop_reason":"tool_use"`) {
		t.Error("stop_reason should NOT be tool_use when truncated")
	}
}

// FINDING 1 (Fix Round 2): Tool block cannot remain open when text block opens.
// Regression test for tool-then-text ordering with depth counter assertion.
func TestToolThenTextNesting(t *testing.T) {
	r := &recorder{}
	tr := NewStreamTranslator(r, "msg_1", "m", func() string { return "gen" })
	tr.Start()

	var tool, text, final Chunk
	json.Unmarshal([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_1","function":{"name":"read","arguments":"{}"}}]}}]}`), &tool)
	json.Unmarshal([]byte(`{"choices":[{"delta":{"content":"Result"}}]}`), &text)
	json.Unmarshal([]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`), &final)

	tr.Chunk(tool)
	tr.Chunk(text)
	tr.Chunk(final)
	tr.Finish()

	names := r.names()
	// Verify strict alternation: at most one block open at a time.
	openCount := 0
	for i, name := range names {
		if name == "content_block_start" {
			openCount++
			if openCount > 1 {
				t.Fatalf("event %d: content_block_start while %d blocks already open; full: %v", i, openCount-1, names)
			}
		} else if name == "content_block_stop" {
			openCount--
			if openCount < 0 {
				t.Fatalf("event %d: content_block_stop with no open block; full: %v", i, names)
			}
		}
	}
	if openCount != 0 {
		t.Fatalf("at end, %d blocks still open; full: %v", openCount, names)
	}

	// Count: one tool and one text block should produce 2 starts and 2 stops.
	starts := 0
	stops := 0
	for _, name := range names {
		if name == "content_block_start" {
			starts++
		} else if name == "content_block_stop" {
			stops++
		}
	}
	if starts != 2 || stops != 2 {
		t.Fatalf("expected 2 content_block_start and 2 content_block_stop, got %d starts and %d stops; full: %v", starts, stops, names)
	}
}
