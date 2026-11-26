package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

var at = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func TestEmptyThenSaved(t *testing.T) {
	dir := t.TempDir()

	s, st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if st.Status.Threads == nil {
		t.Fatalf("empty state is %+v", st)
	}

	st.Status.Version, st.Status.LastAlive = "0.3.0", at
	st.Status.Threads["job/1"] = Thread{Channel: "C1", TS: "1.2"}
	st.Status.Notify.Jobs = map[string]*domain.JobThread{"1": {Device: "a", Posted: true}}

	if err := s.SaveStatus(st.Status); err != nil {
		t.Fatal(err)
	}

	accounts := domain.MembersState{Members: map[string]*domain.MemberRecord{"ABCD": {Slack: "U1", Username: "x"}}}
	if err := s.SaveAccounts(accounts); err != nil {
		t.Fatal(err)
	}

	// A later status save keeps the accounts.
	if err := s.SaveStatus(st.Status); err != nil {
		t.Fatal(err)
	}

	_, back, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if back.Status.Version != "0.3.0" || !back.Status.LastAlive.Equal(at) || back.Status.Threads["job/1"].TS != "1.2" ||
		!back.Status.Notify.Jobs["1"].Posted || back.Accounts.Members["ABCD"].Username != "x" {
		t.Errorf("read back %+v", back)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the directory holds %d files, want only %s", len(entries), FileName)
	}
}

// TestReadsSchema1 is the file 0.2.x wrote, as found on the test server.
func TestReadsSchema1(t *testing.T) {
	dir := t.TempDir()
	old := `{"schema": 1, "version": "0.2.1", "last_alive": "2026-11-25T20:21:46Z",
		"last_applied": "2026-11-25T20:21:46Z", "threads": {"job/9": {"channel": "C1", "ts": "5.6"}}, "notify": {}}`

	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	s, st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if st.Status.Version != "0.2.1" || st.Status.Threads["job/9"].TS != "5.6" || st.Status.LastAlive.IsZero() {
		t.Errorf("schema 1 read as %+v", st.Status)
	}

	if err := s.SaveAccounts(domain.MembersState{}); err != nil {
		t.Fatal(err)
	}

	if _, again, err := Open(dir); err != nil || again.Status.Threads["job/9"].TS != "5.6" {
		t.Errorf("after the first save, the status is %+v (%v)", again.Status, err)
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
