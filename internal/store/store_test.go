package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

func TestEmptyThenSaved(t *testing.T) {
	dir := t.TempDir()

	s, st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if st.Schema != Schema || st.Threads == nil {
		t.Fatalf("empty state is %+v", st)
	}

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	st.Version, st.LastAlive = "0.2.0", at
	st.Threads["job/1"] = Thread{Channel: "C1", TS: "1.2"}
	st.Notify.Jobs = map[string]*domain.JobThread{"1": {Device: "a", Posted: true}}

	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}

	_, back, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if back.Version != "0.2.0" || !back.LastAlive.Equal(at) || back.Threads["job/1"].TS != "1.2" || !back.Notify.Jobs["1"].Posted {
		t.Errorf("read back %+v", back)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only %s", len(entries), FileName)
	}
}

func TestRefusesAnotherSchema(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"schema":99}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Open(dir); !errors.Is(err, ErrSchema) {
		t.Errorf("opened schema 99: %v", err)
	}
}

func TestRefusesBrokenJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(`{"schema":`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Open(dir); err == nil {
		t.Error("opened a broken file")
	}
}
