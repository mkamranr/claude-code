package translate

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToOpenAI converts an Anthropic Messages request into an OpenAI chat
// completions request. defaultModel is used when the request omits one.
func ToOpenAI(req map[string]any, defaultModel string) (map[string]any, error) {
	out := map[string]any{}

	model := defaultModel
	if m, ok := req["model"].(string); ok && m != "" {
		model = m
	}
	out["model"] = model

	for _, k := range []string{"max_tokens", "temperature", "top_p", "stream"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	if v, ok := req["stop_sequences"]; ok {
		out["stop"] = v
	}

	msgs := []any{}
	if sys, ok := req["system"]; ok {
		if s := flattenText(sys); s != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": s})
		}
	}
	inMsgs, _ := req["messages"].([]any)
	for i, raw := range inMsgs {
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages[%d] is not an object", i)
		}
		role, _ := m["role"].(string)
		converted, err := convertMessage(role, m["content"])
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		msgs = append(msgs, converted...)
	}
	out["messages"] = msgs

	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		converted := make([]any, 0, len(tools))
		for _, raw := range tools {
			t, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn := map[string]any{"name": t["name"]}
			if d, ok := t["description"]; ok {
				fn["description"] = d
			}
			if s, ok := t["input_schema"]; ok {
				fn["parameters"] = s
			} else {
				fn["parameters"] = map[string]any{"type": "object"}
			}
			converted = append(converted, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = converted
	}

	if tc, ok := req["tool_choice"]; ok {
		if v := convertToolChoice(tc); v != nil {
			out["tool_choice"] = v
		}
	}
	return out, nil
}

// convertMessage may expand one Anthropic message into several OpenAI ones,
// because tool results ride inside a user message on the Anthropic side but
// need their own role:"tool" messages on the OpenAI side.
func convertMessage(role string, content any) ([]any, error) {
	if s, ok := content.(string); ok {
		return []any{map[string]any{"role": role, "content": s}}, nil
	}
	blocks, ok := content.([]any)
	if !ok {
		return []any{map[string]any{"role": role, "content": ""}}, nil
	}

	var text strings.Builder
	var parts []any
	var toolCalls []any
	var toolResults []any
	hasImage := false

	for _, raw := range blocks {
		b, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch t, _ := b["type"].(string); t {
		case "text":
			s, _ := b["text"].(string)
			text.WriteString(s)
			parts = append(parts, map[string]any{"type": "text", "text": s})
		case "image":
			if u := imageDataURL(b); u != "" {
				hasImage = true
				parts = append(parts, map[string]any{
					"type": "image_url", "image_url": map[string]any{"url": u},
				})
			}
		case "tool_use":
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			args, err := json.Marshal(b["input"])
			if err != nil {
				args = []byte("{}")
			}
			call := map[string]any{"id": id, "type": "function"}
			call["function"] = map[string]any{"name": name, "arguments": string(args)}
			toolCalls = append(toolCalls, call)
		case "tool_result":
			id, _ := b["tool_use_id"].(string)
			toolResults = append(toolResults, map[string]any{
				"role": "tool", "tool_call_id": id, "content": flattenText(b["content"]),
			})
		case "thinking", "redacted_thinking":
			// No OpenAI equivalent on input. Dropped deliberately.
		}
	}

	out := make([]any, 0, len(toolResults)+1)
	out = append(out, toolResults...)

	// A user turn that carried nothing but tool results adds no further message.
	if len(toolResults) > 0 && text.Len() == 0 && len(toolCalls) == 0 && !hasImage {
		return out, nil
	}

	msg := map[string]any{"role": role}
	if hasImage {
		msg["content"] = parts
	} else {
		msg["content"] = text.String()
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	return append(out, msg), nil
}

func convertToolChoice(tc any) any {
	m, ok := tc.(map[string]any)
	if !ok {
		return nil
	}
	switch t, _ := m["type"].(string); t {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": m["name"]},
		}
	}
	return nil
}

// flattenText reduces a string or a block array to plain text.
func flattenText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, raw := range c {
			if m, ok := raw.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					b.WriteString(s)
				}
			}
		}
		return b.String()
	}
	return ""
}

func imageDataURL(block map[string]any) string {
	src, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	switch t, _ := src["type"].(string); t {
	case "base64":
		media, _ := src["media_type"].(string)
		data, _ := src["data"].(string)
		if media == "" || data == "" {
			return ""
		}
		return "data:" + media + ";base64," + data
	case "url":
		u, _ := src["url"].(string)
		return u
	}
	return ""
}
