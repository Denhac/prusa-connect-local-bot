package connect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

const device = "0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

func frame(t *testing.T, name string) domain.Event {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "server", "0.1.0-beta.2", "events", name))
	if err != nil {
		t.Fatal(err)
	}

	ev, ok := Decode(raw)
	if !ok {
		t.Fatalf("%s was not decoded", name)
	}

	if ev.Device != device {
		t.Errorf("%s names device %q", name, ev.Device)
	}

	return ev
}

func TestDecodeJobs(t *testing.T) {
	started := frame(t, "job.started.json")
	if started.Kind != domain.EventJobStarted || started.Job.ID == "" || started.Job.File != "bracket.bgcode" {
		t.Errorf("started is %+v", started)
	}

	settled := frame(t, "job.settled.json")
	j := settled.Job

	switch {
	case settled.Kind != domain.EventJobSettled, j.ID != started.Job.ID, j.Outcome != domain.OutcomeFinished:
		t.Errorf("settled is %+v", settled)
	case !j.PrintedKnown || j.Printed != 124*time.Minute, !j.FilamentKnown || j.FilamentMM != 10250.5:
		t.Errorf("settled facts are %+v", j)
	case !j.Ended.Equal(time.Date(2026, 11, 25, 21, 4, 0, 0, time.UTC)):
		t.Errorf("ended %v", j.Ended)
	}
}

func TestDecodeIssues(t *testing.T) {
	ev := frame(t, "issues.changed.json")
	if ev.Kind != domain.EventIssuesChanged || len(ev.Issues) != 1 || ev.Issues[0].Reason != "awaiting_operator" ||
		ev.Dialog == nil || ev.Dialog.Title != "Filament runout" || !slices.Equal(ev.Dialog.Buttons, []string{"Continue"}) {
		t.Errorf("issues is %+v", ev)
	}

	if cleared := frame(t, "issues.cleared.json"); len(cleared.Issues) != 0 || cleared.Dialog != nil {
		t.Errorf("cleared is %+v", cleared)
	}
}

func TestDecodeTheRest(t *testing.T) {
	cases := map[string]domain.Event{
		"state.changed.json":    {Kind: domain.EventStateChanged, State: "ATTENTION"},
		"presence.changed.json": {Kind: domain.EventPresence, Connected: false},
		"departed.json":         {Kind: domain.EventDeparted, Connected: false},
		"arrived.json":          {Kind: domain.EventArrived, Connected: true},
		"record.changed.json":   {Kind: domain.EventRecordChanged, Name: "MK4S-1"},
	}

	for name, want := range cases {
		got := frame(t, name)
		if got.Kind != want.Kind || got.State != want.State || got.Connected != want.Connected || got.Name != want.Name {
			t.Errorf("%s is %+v, want %+v", name, got, want)
		}
	}
}

func TestDecodeSkips(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{"type":"com.prusa.connect.device.telemetry.sampled","subject":"device:x","data":{"path":"/state","value":"IDLE"}}`,
		`{"type":"com.prusa.connect.device.job.started","subject":"sink:spools","data":{"job_id":"1"}}`,
		`{"type":"com.prusa.connect.device.job.started","subject":"device:x","data":{}}`,
		`{"type":"com.prusa.connect.device.presence.changed","subject":"device:x","data":{"device_id":"x"}}`,
	} {
		if ev, ok := Decode([]byte(raw)); ok {
			t.Errorf("decoded %s as %+v", raw, ev)
		}
	}
}

func TestTypesAreSubscribable(t *testing.T) {
	if len(Types()) != 8 || len(Types()) > 64 {
		t.Errorf("Types is %v", Types())
	}
}
