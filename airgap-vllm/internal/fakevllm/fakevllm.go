// Package fakevllm is an in-process stand-in for a vLLM server, used by tests
// that need to exercise probing, mode selection and streaming without a model.
package fakevllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Config struct {
	// MessagesAPI reports whether this build exposes the native Anthropic
	// endpoint. When false, /v1/messages answers 404 as an older vLLM does.
	MessagesAPI bool
	Models      []string
	// ChatCompletion is the raw body returned by /v1/chat/completions when the
	// request is not streaming.
	ChatCompletion string
	// StreamChunks are raw SSE `data:` payloads returned when stream is true.
	// A terminating [DONE] is appended automatically.
	StreamChunks []string
	// MessagesBody is the raw body returned by a native /v1/messages call.
	MessagesBody string
	HasTokenize  bool
	TokenCount   int
}

type Recorded struct {
	Method string
	Path   string
	Body   string
}

type Server struct {
	http *httptest.Server
	cfg  Config

	mu       sync.Mutex
	requests []Recorded
}

func New(cfg Config) *Server {
	s := &Server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.record(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.record(s.handleChat))
	mux.HandleFunc("/v1/messages", s.record(s.handleMessages))
	mux.HandleFunc("/tokenize", s.record(s.handleTokenize))
	s.http = httptest.NewServer(mux)
	return s
}

func (s *Server) URL() string { return s.http.URL }
func (s *Server) Close()      { s.http.Close() }

func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Recorded, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *Server) record(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, Recorded{r.Method, r.URL.Path, string(body)})
		s.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		next(w, r)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	data := make([]map[string]any, 0, len(s.cfg.Models))
	for _, m := range s.cfg.Models {
		data = append(data, map[string]any{"id": m, "object": "model"})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &req)

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range s.cfg.StreamChunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			if flusher != nil {
				flusher.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, s.cfg.ChatCompletion)
}

func (s *Server) handleMessages(w http.ResponseWriter, _ *http.Request) {
	if !s.cfg.MessagesAPI {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	body := s.cfg.MessagesBody
	if body == "" {
		body = `{"id":"msg_1","type":"message","role":"assistant","content":[]}`
	}
	io.WriteString(w, body)
}

func (s *Server) handleTokenize(w http.ResponseWriter, _ *http.Request) {
	if !s.cfg.HasTokenize {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"count": s.cfg.TokenCount})
}
