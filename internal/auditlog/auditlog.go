// Package auditlog keeps the bot's account lines (component=audit) in a small
// file in the module's data directory, so they outlive a reboot.
//
// The server's journal on a Raspberry Pi is usually in memory only
// (journald's Storage=volatile), which spares the SD card and loses every
// line at a reboot. Account changes are the lines worth keeping, and they are
// few: a handful a day. So they go to the journal as before, and here too.
//
// The file is appended to, never rewritten, and is not synced line by line:
// the filesystem writes it within seconds. At MaxSize it moves to
// audit.log.1, and the one before that goes.
package auditlog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The file's name in the data directory, and its size before it moves aside.
const (
	FileName = "audit.log"
	MaxSize  = 1 << 20
)

// File is the audit file, safe for one logger's concurrent writes.
type File struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// Open opens dir/audit.log for appending, making it if it is not there.
func Open(dir string, maxSize int64) (*File, error) {
	a := &File{path: filepath.Join(dir, FileName), max: maxSize}
	if err := a.open(); err != nil {
		return nil, err
	}

	return a, nil
}

func (a *File) open() error {
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("auditlog: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return fmt.Errorf("auditlog: %w", err)
	}

	a.f, a.size = f, info.Size()

	return nil
}

// Write appends one line, first moving a full file aside.
func (a *File) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.f == nil {
		return 0, os.ErrClosed
	}

	if a.size > 0 && a.size+int64(len(p)) > a.max {
		if err := a.rotate(); err != nil {
			return 0, err
		}
	}

	n, err := a.f.Write(p)
	a.size += int64(n)

	if err != nil {
		return n, fmt.Errorf("auditlog: %w", err)
	}

	return n, nil
}

func (a *File) rotate() error {
	if err := a.f.Close(); err != nil {
		return fmt.Errorf("auditlog: %w", err)
	}

	a.f = nil

	if err := os.Rename(a.path, a.path+".1"); err != nil {
		return fmt.Errorf("auditlog: %w", err)
	}

	return a.open()
}

// Close closes the file. Later writes fail.
func (a *File) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.f == nil {
		return nil
	}

	err := a.f.Close()
	a.f = nil

	if err != nil {
		return fmt.Errorf("auditlog: %w", err)
	}

	return nil
}
