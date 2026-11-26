package domain

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeFleet struct {
	printers []Printer
	err      error
}

func (f fakeFleet) Printers(context.Context) ([]Printer, error) { return f.printers, f.err }

var start = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func newBot(fleet Fleet) *Bot {
	health := func() Health {
		return Health{
			Version: "0.1.0", SDKVersion: "v0.1.0-beta.2", Started: start,
			Slack: SlackStatus{State: SlackConnected, Since: start},
		}
	}

	return NewBot(fleet, health, func() time.Time { return start.Add(90 * time.Minute) })
}

// TestEveryCommandHasAHandler holds the command table and the handlers to
// each other, so help never lists a command that does nothing.
func TestEveryCommandHasAHandler(t *testing.T) {
	handlers := newBot(fakeFleet{}).handlers()

	for _, c := range Commands() {
		if _, ok := handlers[c.Name]; !ok {
			t.Errorf("command %q has no handler", c.Name)
		}

		if c.Summary == "" {
			t.Errorf("command %q has no summary", c.Name)
		}
	}

	for name := range handlers {
		if _, ok := Lookup(name); !ok {
			t.Errorf("handler %q is not in Commands", name)
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]struct {
		name string
		args []string
	}{
		"":                   {"help", nil},
		"   ":                {"help", nil},
		"BOT":                {"bot", []string{}},
		"status  MK4S-1 now": {"status", []string{"MK4S-1", "now"}},
	}

	for text, want := range cases {
		name, args := Parse(text)
		if name != want.name || !slices.Equal(args, want.args) {
			t.Errorf("Parse(%q) = %q %q, want %q %q", text, name, args, want.name, want.args)
		}
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	help := strings.Join(Help().Lines, "\n")

	for _, c := range Commands() {
		if !strings.Contains(help, SlashCommand+" "+c.Name) {
			t.Errorf("help does not list %s", c.Name)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	got := newBot(fakeFleet{}).Handle(context.Background(), Request{Text: "frobnicate"})
	if !strings.Contains(strings.Join(got.Lines, " "), `"frobnicate"`) {
		t.Errorf("answer %+v", got)
	}
}

func TestBotStatus(t *testing.T) {
	fleet := fakeFleet{printers: []Printer{{ID: "a", Connected: true}, {ID: "b"}}}
	got := newBot(fleet).Handle(context.Background(), Request{Text: "bot"})

	want := map[string]string{
		"Version":     "0.1.0 (SDK v0.1.0-beta.2)",
		"Running for": "1 h 30 min",
		"Slack":       "connected for 1 h 30 min",
		"Printers":    "2 visible, 1 online",
	}
	for _, f := range got.Fields {
		if w, ok := want[f.Label]; ok && f.Value != w {
			t.Errorf("%s is %q, want %q", f.Label, f.Value, w)
		}

		delete(want, f.Label)
	}

	if len(want) > 0 {
		t.Errorf("fields missing: %v", want)
	}
}

func TestBotStatusWithoutTheFleet(t *testing.T) {
	got := newBot(fakeFleet{err: errors.New("not ready")}).Handle(context.Background(), Request{Text: "bot"})

	for _, f := range got.Fields {
		if f.Label == "Printers" && f.Value != "cannot read the printers right now" {
			t.Errorf("Printers is %q", f.Value)
		}
	}
}

func TestSince(t *testing.T) {
	cases := map[time.Duration]string{
		20 * time.Second:           "less than a minute",
		5 * time.Minute:            "5 min",
		26*time.Hour + time.Minute: "26 h 1 min",
		72 * time.Hour:             "3 days",
	}
	for d, want := range cases {
		if got := since(start.Add(d), start); got != want {
			t.Errorf("since %v = %q, want %q", d, got, want)
		}
	}

	if since(start, time.Time{}) != "unknown" {
		t.Error("a zero moment is not unknown")
	}
}
