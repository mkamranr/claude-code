// Package probe detects what a vLLM deployment supports, so the bridge can
// choose passthrough or translation without the operator having to know their
// server's version.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Capabilities struct {
	MessagesAPI bool
	Tokenize    bool
	Models      []string
	// MaxModelLen is the context length vLLM reports per model, when it does.
	MaxModelLen map[string]int
}

func (c Capabilities) HasModel(id string) bool {
	for _, m := range c.Models {
		if m == id {
			return true
		}
	}
	return false
}

// ResolveMode turns the configured mode plus detected capabilities into the
// lane actually used. An explicit mode always wins over detection.
func ResolveMode(configured string, caps Capabilities) string {
	if configured == "messages" || configured == "openai" {
		return configured
	}
	if caps.MessagesAPI {
		return "messages"
	}
	return "openai"
}

// Detect queries the server. Reaching /v1/models is treated as the liveness
// check: if that fails, the server is considered unreachable and the error
// names the URL so the operator can correct it.
func Detect(ctx context.Context, client *http.Client, baseURL, apiKey string) (Capabilities, error) {
	base := strings.TrimRight(baseURL, "/")
	var caps Capabilities

	models, lens, err := FetchModels(ctx, client, base, apiKey)
	if err != nil {
		return caps, fmt.Errorf("cannot reach vLLM at %s: %w", base, err)
	}
	caps.Models = models
	caps.MaxModelLen = lens

	// /v1/messages probe: any error surfaces. This decides which lane runs,
	// so a wrong answer is expensive and must never be guessed.
	present, err := endpointExists(ctx, client, base+"/v1/messages", apiKey,
		`{"model":"probe","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`)
	if err != nil {
		return caps, fmt.Errorf("probing %s/v1/messages failed after /v1/models succeeded: %w", base, err)
	}
	caps.MessagesAPI = present

	// /tokenize probe: degrade on transport error, except on context error.
	// This only decides whether token counts are exact or estimated.
	present, err = endpointExists(ctx, client, base+"/tokenize", apiKey,
		`{"prompt":"x"}`)
	switch {
	case err != nil && ctx.Err() != nil:
		return caps, fmt.Errorf("probing %s/tokenize aborted: %w", base, err)
	case err != nil:
		caps.Tokenize = false // optional capability; estimate instead
	default:
		caps.Tokenize = present
	}

	return caps, nil
}

// FetchModels queries /v1/models and returns the model ids the server is
// currently serving, plus the context length (max_model_len) vLLM reports for
// each model that includes it. It is cheap enough to call on demand — a
// single GET — unlike Detect, which also probes /v1/messages and /tokenize.
func FetchModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, map[string]int, error) {
	base := strings.TrimRight(baseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, nil, err
	}
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("/v1/models returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID          string `json:"id"`
			MaxModelLen int    `json:"max_model_len"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, nil, fmt.Errorf("parsing /v1/models: %w", err)
	}
	ids := make([]string, 0, len(body.Data))
	lens := map[string]int{}
	for _, m := range body.Data {
		ids = append(ids, m.ID)
		if m.MaxModelLen > 0 {
			lens[m.ID] = m.MaxModelLen
		}
	}
	return ids, lens, nil
}

// endpointExists distinguishes "this route is not implemented" from "this route
// exists and rejected my probe payload". Only the former means absent.
// Returns (bool, error): the bool is meaningful only when error is nil.
// A non-2xx response is a successful probe with nil error.
func endpointExists(ctx context.Context, client *http.Client, url, apiKey, payload string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false, nil
	}
	return true, nil
}

func auth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
