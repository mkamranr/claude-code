package translate

// EventWriter receives named SSE events. The server implements it against an
// http.ResponseWriter; tests implement it as a recorder.
type EventWriter interface {
	Event(name string, data any) error
}

// StreamTranslator converts a sequence of OpenAI chunks into a well-formed
// Anthropic event stream. Anthropic requires content blocks to be explicitly
// opened, indexed and closed in order, while OpenAI emits flat deltas, so this
// type carries the block bookkeeping.
//
// Call Start once, Chunk per received chunk, then Finish exactly once.
type StreamTranslator struct {
	w         EventWriter
	messageID string
	model     string
	genID     func() string

	started      bool
	closed       bool
	textOpen     bool
	textIndex    int
	nextIndex    int
	toolBlocks   map[int]int // OpenAI tool_call index -> Anthropic block index
	toolOpen     int         // Anthropic block index of currently open tool block, or -1
	inputTokens  int
	outputTokens int
	stopReason   string
}

func NewStreamTranslator(w EventWriter, messageID, model string, genID func() string) *StreamTranslator {
	return &StreamTranslator{
		w: w, messageID: messageID, model: model, genID: genID,
		toolBlocks: map[int]int{}, toolOpen: -1,
	}
}

func (t *StreamTranslator) Start() error {
	if t.started {
		return nil
	}
	t.started = true
	return t.w.Event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": t.messageID, "type": "message", "role": "assistant",
			"model": t.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": t.inputTokens, "output_tokens": 0},
		},
	})
}

func (t *StreamTranslator) Chunk(c Chunk) error {
	if c.Usage != nil {
		if c.Usage.PromptTokens > 0 {
			t.inputTokens = c.Usage.PromptTokens
		}
		if c.Usage.CompletionTokens > 0 {
			t.outputTokens = c.Usage.CompletionTokens
		}
	}
	if err := t.Start(); err != nil {
		return err
	}
	if len(c.Choices) == 0 {
		return nil
	}
	ch := c.Choices[0]

	if ch.Delta.Content != "" {
		if !t.textOpen {
			if err := t.closeTool(); err != nil {
				return err
			}
			t.textIndex = t.nextIndex
			t.nextIndex++
			t.textOpen = true
			if err := t.w.Event("content_block_start", map[string]any{
				"type": "content_block_start", "index": t.textIndex,
				"content_block": map[string]any{"type": "text", "text": ""},
			}); err != nil {
				return err
			}
		}
		if err := t.w.Event("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": t.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": ch.Delta.Content},
		}); err != nil {
			return err
		}
	}

	for _, tc := range ch.Delta.ToolCalls {
		idx, known := t.toolBlocks[tc.Index]
		if !known {
			// Anthropic blocks nest strictly; close text and any open tool before opening a new tool.
			if err := t.closeText(); err != nil {
				return err
			}
			if err := t.closeTool(); err != nil {
				return err
			}
			idx = t.nextIndex
			t.nextIndex++
			t.toolBlocks[tc.Index] = idx
			t.toolOpen = idx
			id := tc.ID
			if id == "" {
				id = t.genID()
			}
			if err := t.w.Event("content_block_start", map[string]any{
				"type": "content_block_start", "index": idx,
				"content_block": map[string]any{
					"type": "tool_use", "id": id,
					"name": tc.Function.Name, "input": map[string]any{},
				},
			}); err != nil {
				return err
			}
		}
		if tc.Function.Arguments != "" {
			if err := t.w.Event("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{
					"type": "input_json_delta", "partial_json": tc.Function.Arguments,
				},
			}); err != nil {
				return err
			}
		}
	}

	if ch.FinishReason != "" {
		t.stopReason = MapStopReason(ch.FinishReason)
	}
	return nil
}

func (t *StreamTranslator) closeTool() error {
	if t.toolOpen == -1 {
		return nil
	}
	idx := t.toolOpen
	t.toolOpen = -1
	return t.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": idx,
	})
}

func (t *StreamTranslator) closeText() error {
	if !t.textOpen {
		return nil
	}
	t.textOpen = false
	return t.w.Event("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": t.textIndex,
	})
}

// Finish closes every open block and terminates the message. It is safe to
// call more than once so that both the normal path and a deferred cleanup can
// invoke it.
func (t *StreamTranslator) Finish() error {
	if err := t.Start(); err != nil {
		return err
	}
	if t.closed {
		return nil
	}
	t.closed = true

	if err := t.closeText(); err != nil {
		return err
	}
	if err := t.closeTool(); err != nil {
		return err
	}

	stop := t.stopReason
	if stop == "" {
		stop = "end_turn"
	}
	if stop == "end_turn" && len(t.toolBlocks) > 0 {
		stop = "tool_use"
	}
	usage := map[string]any{"output_tokens": t.outputTokens}
	if t.inputTokens > 0 {
		usage["input_tokens"] = t.inputTokens
	}
	if err := t.w.Event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": usage,
	}); err != nil {
		return err
	}
	return t.w.Event("message_stop", map[string]any{"type": "message_stop"})
}
