package auditlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendsAndRotates appends lines, keeps them over a reopen, and moves a
// full file aside to audit.log.1.
func TestAppendsAndRotates(t *testing.T) {
	dir := t.TempDir()

	a, err := Open(dir, 64)
	if err != nil {
		t.Fatal(err)
	}

	line := strings.Repeat("x", 29) + "\n" // 30 bytes

	for range 2 {
		if _, err := a.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	_ = a.Close()

	a, err = Open(dir, 64) // reopened: the size is read back
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}

	_ = a.Close()

	now, _ := os.ReadFile(filepath.Join(dir, FileName))
	old, _ := os.ReadFile(filepath.Join(dir, FileName+".1"))

	if len(old) != 60 || len(now) != 30 {
		t.Errorf("audit.log.1 has %d bytes and audit.log %d", len(old), len(now))
	}

	if info, _ := os.Stat(filepath.Join(dir, FileName)); info.Mode().Perm() != 0o600 {
		t.Errorf("the file is %v", info.Mode().Perm())
	}

	if _, err := a.Write([]byte(line)); err == nil {
		t.Error("a write after Close worked")
	}
}
