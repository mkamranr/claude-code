package translate

import "encoding/json"

// FromOpenAI converts a non-streaming chat completion into an Anthropic
// Messages response. genID supplies an id for any tool call that arrives
// without one, which strict clients require.
func FromOpenAI(r OpenAIResponse, fallbackModel string, genID func() string) map[string]any {
	model := r.Model
	if model == "" {
		model = fallbackModel
	}
	id := r.ID
	if id == "" {
		id = genID()
	}

	content := []any{}
	stop := "end_turn"

	if len(r.Choices) > 0 {
		ch := r.Choices[0]
		if ch.Message.Content != "" {
			content = append(content, map[string]any{
				"type": "text", "text": ch.Message.Content,
			})
		}
		for _, tc := range ch.Message.ToolCalls {
			callID := tc.ID
			if callID == "" {
				callID = genID()
			}
			input := map[string]any{}
			if tc.Function.Arguments != "" {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err == nil {
					input = parsed
				}
			}
			content = append(content, map[string]any{
				"type": "tool_use", "id": callID, "name": tc.Function.Name, "input": input,
			})
		}
		stop = MapStopReason(ch.FinishReason)
		// Some servers report "stop" even when they emitted tool calls.
		if stop == "end_turn" && len(ch.Message.ToolCalls) > 0 {
			stop = "tool_use"
		}
	}

	return map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  r.Usage.PromptTokens,
			"output_tokens": r.Usage.CompletionTokens,
		},
	}
}
