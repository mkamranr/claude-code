package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCanonicalFolderExpandsShortNamesAndDriveCase(t *testing.T) {
	long := filepath.Join(t.TempDir(), "a folder with a long name")
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	want := canonicalFolder(long)

	// The 8.3 alias, when the volume generates them.
	src, _ := syscall.UTF16PtrFromString(long)
	buf := make([]uint16, syscall.MAX_PATH)
	if n, err := syscall.GetShortPathName(src, &buf[0], uint32(len(buf))); err == nil && n > 0 {
		short := syscall.UTF16ToString(buf[:n])
		if got := canonicalFolder(short); got != want {
			t.Errorf("canonicalFolder(%q) = %q, want %q", short, got, want)
		}
	}

	lowerDrive := strings.ToLower(want[:1]) + want[1:]
	if got := canonicalFolder(lowerDrive); got != want {
		t.Errorf("canonicalFolder(%q) = %q, want %q", lowerDrive, got, want)
	}
	if want[0] < 'A' || want[0] > 'Z' {
		t.Errorf("drive letter not upper-cased: %q", want)
	}
}
