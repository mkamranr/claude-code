package desktop

import (
	"path/filepath"
	"strings"
	"syscall"
)

// canonicalFolder returns one spelling per folder. Claude Code keys session
// storage on the literal cwd string, so C:\Users\JOHNDO~1\app (an 8.3 short
// name) or c:\users\johndoe\app would otherwise start an empty history for a
// folder that already has chats. Short names are expanded and the drive letter
// is upper-cased; anything that fails falls back to the absolute path.
func canonicalFolder(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if src, err := syscall.UTF16PtrFromString(abs); err == nil {
		buf := make([]uint16, syscall.MAX_LONG_PATH)
		if n, err := syscall.GetLongPathName(src, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
			abs = syscall.UTF16ToString(buf[:n])
		}
	}
	if len(abs) >= 2 && abs[1] == ':' {
		abs = strings.ToUpper(abs[:1]) + abs[1:]
	}
	return abs
}
