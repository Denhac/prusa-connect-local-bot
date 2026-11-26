// Package store keeps the module's state in one JSON file in the data
// directory the server grants it (docs/design.md, "State").
//
// Two owners write it: the status-message loop and the account service. Each
// saves its own part, which is encoded while its owner holds its own lock, so
// neither can race the other. Every save writes a temporary file, syncs it,
// renames it over the old one and syncs the directory, so a crash leaves
// either the old state or the new one.
package store

import (
	"bytes"
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

// Schema is the version of the file this code writes. Version 1 (0.2.x) is
// read and turned into version 2; any other stops the start with an error.
const Schema = 2

// FileName is the state file's name in the data directory.
const FileName = "state.json"

// ErrSchema is a state file written by another version of the module.
var ErrSchema = errors.New("store: the state file has a schema this version does not read")

// Thread is where a message lives in Slack.
type Thread struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// Status is the status-message loop's part.
type Status struct {
	// Version is the module version that last wrote the file.
	Version string `json:"version,omitempty"`
	// LastAlive is when the module was last known to run. It is kept every
	// minute in memory and reaches the file at most every 10 minutes, and when
	// the module stops (internal/app, flushEvery).
	LastAlive time.Time `json:"last_alive,omitzero"`
	// LastApplied is when the last event applied was published.
	LastApplied time.Time `json:"last_applied,omitzero"`
	// Threads is each status message posted, by its thread key.
	Threads map[string]Thread  `json:"threads,omitempty"`
	Notify  domain.NotifyState `json:"notify"`
}

// State is the whole file.
type State struct {
	Status   Status              `json:"status"`
	Accounts domain.MembersState `json:"accounts"`
}

// file is the file's own shape.
type file struct {
	Schema   int             `json:"schema"`
	Status   json.RawMessage `json:"status,omitempty"`
	Accounts json.RawMessage `json:"accounts,omitempty"`
}

// Store is the state file. It is safe for concurrent use.
type Store struct {
	path string

	mu       sync.Mutex
	status   json.RawMessage
	accounts json.RawMessage
	// written is what the file holds now, so a save that changes nothing
	// does not write the SD card again.
	written []byte
}

// Open reads the state file in dir, or answers an empty state when there is
// none yet.
func Open(dir string) (*Store, State, error) {
	s := &Store{path: filepath.Join(dir, FileName)}
	st := State{Status: Status{Threads: map[string]Thread{}}}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, st, nil
	}

	if err != nil {
		return nil, State{}, fmt.Errorf("store: read %s: %w", s.path, err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, State{}, fmt.Errorf("store: %s is not valid JSON: %w", s.path, err)
	}

	switch f.Schema {
	case 1:
		// 0.2.x kept the status fields at the top level, beside the schema.
		f.Status = data
	case Schema:
	default:
		return nil, State{}, fmt.Errorf("%w: %s is schema %d, and this version reads 1 and %d", ErrSchema, s.path, f.Schema, Schema)
	}

	if len(f.Status) > 0 {
		if err := json.Unmarshal(f.Status, &st.Status); err != nil {
			return nil, State{}, fmt.Errorf("store: the status in %s: %w", s.path, err)
		}
	}

	if len(f.Accounts) > 0 {
		if err := json.Unmarshal(f.Accounts, &st.Accounts); err != nil {
			return nil, State{}, fmt.Errorf("store: the accounts in %s: %w", s.path, err)
		}
	}

	if st.Status.Threads == nil {
		st.Status.Threads = map[string]Thread{}
	}

	// Keep both parts as they were read, so the first save of one keeps the
	// other.
	s.status, _ = json.Marshal(st.Status)
	s.accounts, _ = json.Marshal(st.Accounts)

	return s, st, nil
}

// SaveStatus writes the status part. The caller holds whatever guards st.
func (s *Store) SaveStatus(st Status) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("store: encode the status: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = data

	return s.write()
}

// SaveAccounts writes the account part. The caller holds whatever guards st.
func (s *Store) SaveAccounts(st domain.MembersState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("store: encode the accounts: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.accounts = data

	return s.write()
}

// write puts both parts on disk. s.mu is held.
func (s *Store) write() error {
	data, err := json.MarshalIndent(file{Schema: Schema, Status: s.status, Accounts: s.accounts}, "", " ")
	if err != nil {
		return fmt.Errorf("store: encode: %w", err)
	}

	data = append(data, '\n')
	if bytes.Equal(data, s.written) {
		return nil
	}

	dir := filepath.Dir(s.path)

	tmp, err := os.CreateTemp(dir, FileName+".*")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}

	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename; this only cleans up a failure.

	if _, err = tmp.Write(data); err == nil {
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

	s.written = data

	return nil
}
