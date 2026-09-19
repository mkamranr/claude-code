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
