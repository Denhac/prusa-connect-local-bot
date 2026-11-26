// Package store keeps the module's state in one JSON file in the data
// directory the server grants it (docs/design.md, "State").
//
// Every save writes a temporary file, syncs it, renames it over the old one and
// syncs the directory, so a crash leaves either the old state or the new one.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// Schema is the version of the file this code writes. A file of another
// version stops the start with a clear error: there are no migrations until
// one is needed.
const Schema = 1

// FileName is the state file's name in the data directory.
const FileName = "state.json"

// ErrSchema is a state file written by another version of the module.
var ErrSchema = errors.New("store: the state file has a schema this version does not read")

// Thread is where a message lives in Slack.
type Thread struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// State is everything the file holds.
type State struct {
	Schema int `json:"schema"`
	// Version is the module version that last wrote the file.
	Version string `json:"version,omitempty"`
	// LastAlive is written every minute while the module runs.
	LastAlive time.Time `json:"last_alive,omitzero"`
	// LastApplied is when the last event applied was published.
	LastApplied time.Time `json:"last_applied,omitzero"`
	// Threads is each status message the module posted, by its thread key.
	Threads map[string]Thread  `json:"threads,omitempty"`
	Notify  domain.NotifyState `json:"notify"`
}

// Store is the state file. It is safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open reads the state file in dir, or answers an empty state when there is
// none yet.
func Open(dir string) (*Store, State, error) {
	s := &Store{path: filepath.Join(dir, FileName)}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, State{Schema: Schema, Threads: map[string]Thread{}}, nil
	}

	if err != nil {
		return nil, State{}, fmt.Errorf("store: read %s: %w", s.path, err)
	}

	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, State{}, fmt.Errorf("store: %s is not valid JSON: %w", s.path, err)
	}

	if st.Schema != Schema {
		return nil, State{}, fmt.Errorf("%w: %s is schema %d, and this version reads %d", ErrSchema, s.path, st.Schema, Schema)
	}

	if st.Threads == nil {
		st.Threads = map[string]Thread{}
	}

	return s, st, nil
}

// Save writes the state.
func (s *Store) Save(st State) error {
	st.Schema = Schema

	data, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return fmt.Errorf("store: encode: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	dir := filepath.Dir(s.path)

	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename; this only cleans up a failure.

	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}

	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("store: write %s: %w", tmp.Name(), err)
	}

	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("store: %w", err)
	}

	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // the rename is durable once the directory is
		_ = d.Close()
	}

	return nil
}
