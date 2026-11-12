//go:build !windows

package desktop

import "path/filepath"

// canonicalFolder returns one spelling per folder; see folder_windows.go.
func canonicalFolder(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			return real
		}
		return abs
	}
	return p
}
