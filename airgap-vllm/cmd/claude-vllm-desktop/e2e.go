//go:build e2e

// End-to-end test hook, compiled in only with -tags e2e and never shipped:
// an endpoint on 127.0.0.1:$CLAUDE_VLLM_DESKTOP_E2E_PORT that evaluates a JS
// expression in the page and returns its JSON-encoded value. It lets a script
// drive the real window (real bindings, real claude.exe, real bridge) on
// machines where neither UI Automation nor WebView2 remote debugging reach
// the page.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

var e2e = struct {
	sync.Mutex
	seq     int
	waiting map[string]chan string
}{waiting: map[string]chan string{}}

// E2EResult receives an evaluation result from the page.
func (a *App) E2EResult(id, value string) {
	e2e.Lock()
	ch := e2e.waiting[id]
	delete(e2e.waiting, id)
	e2e.Unlock()
	if ch != nil {
		ch <- value
	}
}

func init() {
	startupHooks = append(startupHooks, func(ctx context.Context, app *App) {
		port := os.Getenv("CLAUDE_VLLM_DESKTOP_E2E_PORT")
		if port == "" {
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/eval", func(w http.ResponseWriter, r *http.Request) {
			expr, _ := io.ReadAll(r.Body)
			e2e.Lock()
			e2e.seq++
			id := fmt.Sprintf("e%d", e2e.seq)
			ch := make(chan string, 1)
			e2e.waiting[id] = ch
			e2e.Unlock()
			idJSON, _ := json.Marshal(id)
			runtime.WindowExecJS(ctx, fmt.Sprintf(`(async () => {
  const done = (v) => window.go.main.App.E2EResult(%s, v);
  try { const v = await (async () => (%s))(); done(JSON.stringify(v === undefined ? null : v)); }
  catch (e) { done("EXCEPTION " + (e && e.stack || e)); }
})()`, idJSON, expr))
			select {
			case v := <-ch:
				io.WriteString(w, v)
			case <-time.After(60 * time.Second):
				http.Error(w, "timeout", http.StatusGatewayTimeout)
			}
		})
		go http.ListenAndServe("127.0.0.1:"+port, mux)
	})
}
