// Command claude-vllm-desktop is a windowed front end for Claude Code against
// a self-hosted vLLM. It runs claude.exe headless through the same loopback
// bridge as claude-vllm; see internal/desktop.
//
// Build (no wails CLI or Node needed):
//
//	go build -tags desktop,production,wv2runtime.error -trimpath \
//	  -ldflags "-s -w -H windowsgui" -o claude-vllm-desktop.exe ./cmd/claude-vllm-desktop
//
// wv2runtime.error matters: Wails' default strategy, when WebView2 is missing,
// is to download it, which can only hang on an air-gapped machine.
package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"claude-vllm/internal/config"
	"claude-vllm/internal/desktop"
)

//go:embed all:frontend
var assets embed.FS

// App is the object bound to the UI as window.go.main.App. The session and
// settings methods come from the embedded *desktop.Core; App adds the ones
// that need the window.
type App struct {
	*desktop.Core
	ctx context.Context
}

// startupHooks run once the window context exists (used by the e2e build).
var startupHooks []func(ctx context.Context, app *App)

type wailsEmitter struct{ app *App }

func (e wailsEmitter) Emit(event string, data any) {
	if e.app.ctx != nil {
		runtime.EventsEmit(e.app.ctx, event, data)
	}
}

// PickFolder opens the native folder chooser. "" means cancelled.
func (a *App) PickFolder(start string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose a project folder",
		DefaultDirectory: start,
	})
}

// InitialFolder is a project folder given on the command line
// (claude-vllm-desktop.exe C:\work\app), or "" to use the last one.
func (a *App) InitialFolder() string {
	if len(os.Args) < 2 {
		return ""
	}
	abs, err := filepath.Abs(os.Args[1])
	if err != nil {
		return ""
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return ""
	}
	return abs
}

// LogDir is where the bridge writes its log, for the settings screen.
func (a *App) LogDir() string {
	d, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "log")
}

// bundledWebView2 returns the Fixed Version runtime shipped next to the exe
// by install.ps1, or "" to fall back to an installed Evergreen runtime.
func bundledWebView2() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(self), "webview2")
	if _, err := os.Stat(filepath.Join(dir, "msedgewebview2.exe")); err == nil {
		return dir
	}
	return ""
}

func main() {
	app := &App{}
	core, err := desktop.NewCore(wailsEmitter{app})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.Core = core

	userData := ""
	if d, err := config.Dir(); err == nil {
		userData = filepath.Join(d, "webview-data")
	}

	messages := windows.DefaultMessages()
	missing := "The WebView2 runtime was not found. Re-run install.ps1 from a bundle " +
		"staged with WEBVIEW2_CAB set, so the webview2 folder is installed next to " +
		"claude-vllm-desktop.exe. The terminal client (claude-vllm) does not need it."
	messages.Webview2NotInstalled = missing
	messages.ContactAdmin = missing
	messages.Error = "Claude vLLM Desktop"

	err = wails.Run(&options.App{
		Title:            "Claude vLLM Desktop",
		Width:            1200,
		Height:           820,
		MinWidth:         760,
		MinHeight:        520,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 250, G: 249, B: 245, A: 255},
		OnStartup: func(ctx context.Context) {
			app.ctx = ctx
			for _, h := range startupHooks {
				h(ctx, app)
			}
		},
		OnShutdown: func(context.Context) { app.Shutdown() },
		Bind:       []any{app},
		Windows: &windows.Options{
			WebviewBrowserPath:  bundledWebView2(),
			WebviewUserDataPath: userData,
			Theme:               windows.SystemDefault,
			Messages:            messages,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
