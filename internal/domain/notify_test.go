package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newNotifier() *Notifier {
	n := NewNotifier(DefaultNotifyConfig(), NotifyState{})
	n.Rebuild(Snapshot{Printers: []Printer{{ID: "a", Name: "MK4S-1", Connected: true, State: "IDLE"}}}, start)

	return n
}

func job(id string) Job {
	return Job{ID: id, File: "bracket.bgcode", Started: start, TimeRemaining: 2*time.Hour + 10*time.Minute}
}

func ended(id, outcome string, after time.Duration) Job {
	j := job(id)
	j.Outcome, j.Ended, j.TimeRemaining = outcome, start.Add(after), 0
	j.FilamentMM, j.FilamentKnown = 10250, true

	return j
}

// says reads the actions as "kind thread: first line", one per line.
func says(actions []Action) string {
	lines := make([]string, 0, len(actions))

	for _, a := range actions {
		line := string(a.Kind) + " " + a.Thread + ": " + a.Message.Plain()
		if a.StillOf != "" {
			line += " [still " + a.StillOf + "]"
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func expect(t *testing.T, step string, got []Action, want string) {
	t.Helper()

	if says(got) != want {
		t.Errorf("%s:\ngot\n%s\nwant\n%s", step, says(got), want)
	}
}

func TestAPrintFromStartToEnd(t *testing.T) {
	n := newNotifier()

	expect(t, "start", n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start),
		"post job/j1: ▶ MK4S-1 started bracket.bgcode")

	expect(t, "the same start again", n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start), "")

	expect(t, "end", n.Apply(Event{Kind: EventJobSettled, Device: "a", Job: ended("j1", OutcomeFinished, 124*time.Minute)}, start),
		"reply job/j1: ✅ Finished in 2 h 04 min · 10.2 m of filament [still a]\n"+
			"update job/j1: ✅ MK4S-1 finished bracket.bgcode")

	expect(t, "the same end again", n.Apply(Event{Kind: EventJobSettled, Device: "a", Job: ended("j1", OutcomeFinished, time.Hour)}, start), "")
}

func TestStartMentionsTimeLeft(t *testing.T) {
	got := newNotifier().Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start)
	if len(got) != 1 || got[0].Message.Lines[1] != "About 2 h 10 min to go." {
		t.Errorf("start said %+v", got)
	}
}

func TestAnEndWithNoStart(t *testing.T) {
	expect(t, "end", newNotifier().Apply(Event{Kind: EventJobSettled, Device: "a", Job: ended("j9", OutcomeCancelled, 5*time.Minute)}, start),
		"post job/j9: ⏹ MK4S-1 cancelled bracket.bgcode (5 min) [still a]")
}

func TestNeedsAPerson(t *testing.T) {
	n := newNotifier()
	waiting := Event{Kind: EventIssuesChanged, Device: "a",
		Issues: []Issue{{Reason: "awaiting_operator", Severity: "error"}},
		Dialog: &Dialog{Title: "Filament runout", Text: "Load new filament."}}

	expect(t, "issue", n.Apply(waiting, start), "")
	expect(t, "too soon", n.Tick(start.Add(10*time.Second)), "")
	expect(t, "after 30 s", n.Tick(start.Add(30*time.Second)),
		"post attention/a/"+unix(start)+": ⚠ MK4S-1 needs a person: Filament runout [still a]")
	expect(t, "only once", n.Tick(start.Add(time.Minute)), "")
	expect(t, "answered", n.Apply(Event{Kind: EventIssuesChanged, Device: "a"}, start.Add(2*time.Minute)),
		"reply attention/a/"+unix(start)+": ✅ Answered.")
}

func TestAQuestionAnsweredAtOncePostsNothing(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventStateChanged, Device: "a", State: "ATTENTION"}, start)
	expect(t, "answered", n.Apply(Event{Kind: EventStateChanged, Device: "a", State: "PRINTING"}, start.Add(5*time.Second)), "")
	expect(t, "later", n.Tick(start.Add(time.Minute)), "")
}

func TestOtherIssuesPostNothing(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventIssuesChanged, Device: "a", Issues: []Issue{{Reason: "disconnected"}, {Reason: "departed"}}}, start)
	expect(t, "later", n.Tick(start.Add(time.Hour)), "")
}

func TestOfflineDuringAPrint(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start)

	expect(t, "away", n.Apply(Event{Kind: EventPresence, Device: "a", Connected: false}, start), "")
	expect(t, "too soon", n.Tick(start.Add(time.Minute)), "")
	expect(t, "after 2 min", n.Tick(start.Add(2*time.Minute)), "reply job/j1: ⚡ MK4S-1 went offline during the print.")
	expect(t, "back", n.Apply(Event{Kind: EventPresence, Device: "a", Connected: true}, start.Add(3*time.Minute)),
		"reply job/j1: 🔌 MK4S-1 is back online.")
}

func TestOfflineWhileIdlePostsNothing(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventDeparted, Device: "a"}, start)
	expect(t, "later", n.Tick(start.Add(time.Hour)), "")
}

func TestAShortDropPostsNothing(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start)
	n.Apply(Event{Kind: EventPresence, Device: "a", Connected: false}, start)
	expect(t, "back soon", n.Apply(Event{Kind: EventPresence, Device: "a", Connected: true}, start.Add(30*time.Second)), "")
	expect(t, "later", n.Tick(start.Add(time.Hour)), "")
}

// TestRebuildAfterARestart is the server or the module restarting during a
// print: the state file knows the start was posted, and the fleet says the
// print ended meanwhile, and another ran entirely in the gap.
func TestRebuildAfterARestart(t *testing.T) {
	n := newNotifier()
	n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("j1")}, start)

	saved := roundTrip(t, n.State())
	later := NewNotifier(DefaultNotifyConfig(), saved)

	got := later.Rebuild(Snapshot{
		Printers: []Printer{{ID: "a", Name: "MK4S-1", Connected: true, State: "PRINTING"}},
		Current:  map[string]*Job{"a": func() *Job { j := job("j3"); return &j }()},
		Ended: map[string][]Job{"a": {
			ended("j1", OutcomeFinished, time.Hour),
			ended("j2", OutcomeFailed, 2*time.Hour),
		}},
	}, start.Add(3*time.Hour))

	expect(t, "rebuild", got,
		"reply job/j1: ✅ Finished in 1 h 00 min · 10.2 m of filament [still a]\n"+
			"update job/j1: ✅ MK4S-1 finished bracket.bgcode\n"+
			"post job/j2: ❌ MK4S-1 failed bracket.bgcode (2 h 00 min) [still a]\n"+
			"post job/j3: ▶ MK4S-1 started bracket.bgcode")

	expect(t, "the same rebuild again", later.Rebuild(Snapshot{
		Printers: []Printer{{ID: "a", Name: "MK4S-1", Connected: true}},
		Current:  map[string]*Job{"a": func() *Job { j := job("j3"); return &j }()},
		Ended:    map[string][]Job{"a": {ended("j1", OutcomeFinished, time.Hour), ended("j2", OutcomeFailed, 2*time.Hour)}},
	}, start.Add(4*time.Hour)), "")

	expect(t, "the events the rebuild already covered", later.Apply(
		Event{Kind: EventJobSettled, Device: "a", Job: ended("j1", OutcomeFinished, time.Hour)}, start.Add(4*time.Hour)), "")
}

func TestPruneKeepsOpenPrints(t *testing.T) {
	cfg := DefaultNotifyConfig()
	cfg.KeepJobs = 3
	n := NewNotifier(cfg, NotifyState{})

	n.Apply(Event{Kind: EventJobStarted, Device: "a", Job: job("open")}, start)

	for i := range 5 {
		j := ended(string(rune('a'+i)), OutcomeFinished, time.Minute)
		j.Started = start.Add(time.Duration(i) * time.Hour)
		n.Apply(Event{Kind: EventJobSettled, Device: "b", Job: j}, start)
	}

	if len(n.State().Jobs) > 3 {
		t.Errorf("kept %d prints", len(n.State().Jobs))
	}

	if _, ok := n.State().Jobs["open"]; !ok {
		t.Error("the open print was forgotten")
	}

	if got := n.Forgotten([]string{"job/open", "job/a", "attention/x/1"}); strings.Join(got, ",") != "job/a,attention/x/1" {
		t.Errorf("forgotten %v", got)
	}
}

func roundTrip(t *testing.T, st NotifyState) NotifyState {
	t.Helper()

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}

	var out NotifyState
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}

	return out
}

func unix(t time.Time) string {
	data, _ := json.Marshal(t.Unix())

	return string(data)
}

func TestStartMessage(t *testing.T) {
	cases := map[string]struct {
		version, last string
		alive         time.Time
		posts         bool
	}{
		"first start":           {"0.2.0", "", time.Time{}, true},
		"settings edit":         {"0.2.0", "0.2.0", start.Add(-20 * time.Second), false},
		"after a gap":           {"0.2.0", "0.2.0", start.Add(-30 * time.Minute), true},
		"a new version at once": {"0.2.1", "0.2.0", start.Add(-20 * time.Second), true},
	}

	for name, c := range cases {
		m, posts := StartMessage(c.version, c.last, c.alive, start)
		if posts != c.posts {
			t.Errorf("%s: posts %v, want %v", name, posts, c.posts)
		}

		if name == "after a gap" && (len(m.Lines) != 2 || !strings.HasPrefix(m.Lines[1], "It stopped some time after ")) {
			t.Errorf("%s said %v", name, m.Lines)
		}
	}
}
