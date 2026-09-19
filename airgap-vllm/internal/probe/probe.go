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

	models, err := fetchModels(ctx, client, base, apiKey)
	if err != nil {
		return caps, fmt.Errorf("cannot reach vLLM at %s: %w", base, err)
	}
	caps.Models = models
	caps.MessagesAPI = endpointExists(ctx, client, base+"/v1/messages", apiKey,
		`{"model":"probe","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`)
	caps.Tokenize = endpointExists(ctx, client, base+"/tokenize", apiKey,
		`{"prompt":"x"}`)
	return caps, nil
}

func fetchModels(ctx context.Context, client *http.Client, base, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/v1/models returned HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("parsing /v1/models: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		out = append(out, m.ID)
	}
	return out, nil
}

// endpointExists distinguishes "this route is not implemented" from "this route
// exists and rejected my probe payload". Only the former means absent.
func endpointExists(ctx context.Context, client *http.Client, url, apiKey, payload string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	auth(req, apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return false
	}
	return true
}

func auth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}
