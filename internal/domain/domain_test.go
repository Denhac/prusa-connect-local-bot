package domain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

// fakeFleet answers from fixed printers and prints.
type fakeFleet struct {
	printers []Printer
	current  map[string]*Job
	history  map[string][]Job
	err      error
}

func (f fakeFleet) Printers(context.Context) ([]Printer, error) { return f.printers, f.err }

func (f fakeFleet) Jobs(_ context.Context, device string) (*Job, *Job, error) {
	return f.current[device], nil, f.err
}

func (f fakeFleet) History(_ context.Context, device string, _ time.Time, limit int) ([]Job, error) {
	h := f.history[device]

	return h[:min(len(h), limit)], f.err
}

// fakeChat keeps what was posted.
type fakeChat struct {
	posts   []Message
	uploads []string
	err     error
}

func (c *fakeChat) Post(_ context.Context, _ string, m Message) (string, error) {
	c.posts = append(c.posts, m)

	return "ts", c.err
}

func (c *fakeChat) Reply(_ context.Context, _, _ string, m Message) (string, error) {
	c.posts = append(c.posts, m)

	return "ts", c.err
}

func (c *fakeChat) Update(context.Context, string, string, Message) error { return c.err }

func (c *fakeChat) Upload(_ context.Context, _, _ string, _ []byte, title string) error {
	c.uploads = append(c.uploads, title)

	return c.err
}

// fakeStills has a camera for the printers listed.
type fakeStills struct {
	cameras map[string]error
}

func (s fakeStills) Has(device string) bool { _, ok := s.cameras[device]; return ok }

func (s fakeStills) Still(_ context.Context, device string, _ bool) ([]byte, error) {
	err, ok := s.cameras[device]
	if !ok {
		return nil, ErrNoCamera
	}

	return []byte{0xff, 0xd8}, err
}

const bots = "C0BOTS"

// fakeUI keeps what a command said, opened and sent.
type fakeUI struct {
	said      []Message
	modals    []Modal
	dms       []Message
	updateErr error
}

func (u *fakeUI) Respond(_ context.Context, m Message) error { u.said = append(u.said, m); return nil }

func (u *fakeUI) OpenModal(_ context.Context, m Modal) (string, error) {
	u.modals = append(u.modals, m)

	return "V1", nil
}

func (u *fakeUI) UpdateModal(_ context.Context, _ string, m Modal) error {
	if u.updateErr != nil {
		return u.updateErr
	}

	u.modals = append(u.modals, m)

	return nil
}

func (u *fakeUI) DM(_ context.Context, m Message) error { u.dms = append(u.dms, m); return nil }

func (u *fakeUI) last() Modal {
	if len(u.modals) == 0 {
		return Modal{}
	}

	return u.modals[len(u.modals)-1]
}

// ask runs a command and answers what it said last.
func ask(b *Bot, r Request) Message {
	ui := &fakeUI{}
	b.Handle(context.Background(), r, ui)

	if len(ui.said) == 0 {
		return Message{}
	}

	return ui.said[len(ui.said)-1]
}

func newBot(fleet Fleet, chat Chat, stills Stills) *Bot {
	return NewBot(BotConfig{
		Fleet: fleet, Chat: chat, Stills: stills, StatusChannel: bots,
		Health: func() Health {
			return Health{
				Version: "0.2.0", SDKVersion: "v0.1.0-beta.2", Started: start,
				Slack:  SlackStatus{State: SlackConnected, Since: start},
				Notify: NotifyStatus{Running: true},
			}
		},
		Now: func() time.Time { return start.Add(90 * time.Minute) },
	})
}

func text(m Message) string {
	parts := append([]string{m.Title}, m.Lines...)
	for _, f := range m.Fields {
		parts = append(parts, f.Label+": "+f.Value)
	}

	return strings.Join(parts, "\n")
}

// TestEveryCommandHasAHandler holds the command table and the handlers to
// each other, so help never lists a command that does nothing.
func TestEveryCommandHasAHandler(t *testing.T) {
	handlers := newBot(fakeFleet{}, nil, nil).handlers()

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

	for in, want := range cases {
		name, args := Parse(in)
		if name != want.name || !slices.Equal(args, want.args) {
			t.Errorf("Parse(%q) = %q %q, want %q %q", in, name, args, want.name, want.args)
		}
	}
}

func helpText(m Message) string {
	var b strings.Builder

	for _, g := range m.Groups {
		b.WriteString(g.Heading + "\n")

		for _, it := range g.Items {
			b.WriteString(it.Code + " " + it.Text + "\n")
		}
	}

	return b.String()
}

// TestHelpListsEveryCommand lists every command for an admin, and the admin
// commands for nobody else.
func TestHelpListsEveryCommand(t *testing.T) {
	forAdmin, forMember := helpText(Help(true)), helpText(Help(false))

	for _, c := range Commands() {
		word := SlashCommand + " " + c.Name + " " // helpText puts a space after every usage
		if !strings.Contains(forAdmin, word) {
			t.Errorf("an admin's help does not list %s", c.Name)
		}

		listed := strings.Contains(forMember, word)
		if listed != (c.Who != Admins) {
			t.Errorf("a member's help lists %s: %v", c.Name, listed)
		}
	}

	if strings.Contains(forMember, groupAdmins) {
		t.Error("a member's help has the admin heading")
	}
}

func TestUnknownCommand(t *testing.T) {
	got := ask(newBot(fakeFleet{}, nil, nil), Request{Text: "frobnicate"})
	if !strings.Contains(text(got), `"frobnicate"`) {
		t.Errorf("answer %+v", got)
	}
}

func TestBotStatus(t *testing.T) {
	fleet := fakeFleet{printers: []Printer{{ID: "a", Connected: true}, {ID: "b"}}}
	got := ask(newBot(fleet, nil, nil), Request{Text: "bot"})

	want := map[string]string{
		"Version":         "0.2.0 (SDK v0.1.0-beta.2)",
		"Running for":     "1 h 30 min",
		"Slack":           "connected for 1 h 30 min",
		"Status messages": "on",
		"Printers":        "2 visible, 1 online",
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
	got := ask(newBot(fakeFleet{err: errors.New("not ready")}, nil, nil), Request{Text: "bot"})
	if !strings.Contains(text(got), "cannot read the printers right now") {
		t.Errorf("answered %s", text(got))
	}
}

// TestMemberCommandsOnlyInBots keeps printer status in the members' channel
// until phase 2 checks membership itself.
func TestMemberCommandsOnlyInBots(t *testing.T) {
	bot := newBot(fakeFleet{printers: []Printer{{ID: "a", Name: "MK4S-1", Connected: true}}}, &fakeChat{}, nil)

	for _, cmd := range []string{"status", "jobs", "snap"} {
		got := ask(bot, Request{Channel: "C0PUBLIC", Text: cmd})
		if !strings.Contains(text(got), "in #bots") {
			t.Errorf("%s outside #bots answered %s", cmd, text(got))
		}
	}

	if got := ask(bot, Request{Channel: "C0PUBLIC", Text: "help"}); strings.Contains(text(got), "Use ") {
		t.Errorf("help was refused outside #bots: %s", text(got))
	}
}

func TestStatus(t *testing.T) {
	fleet := fakeFleet{
		printers: []Printer{
			{ID: "b", Name: "XL", Connected: true, State: "IDLE", Nozzle: Temp{Now: 25, Known: true}, Bed: Temp{Now: 24, Known: true}},
			{ID: "a", Name: "MK4S-1", Connected: true, State: "PRINTING",
				Nozzle: Temp{Now: 215, Target: 215, Known: true}, Bed: Temp{Now: 60, Target: 60, Known: true}},
			{ID: "c", Name: "Mini", Connected: false},
		},
		current: map[string]*Job{"a": {ID: "j1", File: "bracket.bgcode", Progress: 45, TimeRemaining: 70 * time.Minute}},
	}

	got := ask(newBot(fleet, nil, nil), Request{Channel: bots, Text: "status"})
	want := "Printers\n" +
		"Mini: offline\n" +
		"MK4S-1: printing · 45% of bracket.bgcode, 1 h 10 min to go · nozzle 215/215 °C · bed 60/60 °C\n" +
		"XL: idle · nozzle 25 °C · bed 24 °C"

	if text(got) != want {
		t.Errorf("status answered\n%s\nwant\n%s", text(got), want)
	}

	one := ask(newBot(fleet, nil, nil), Request{Channel: bots, Text: "status mk4"})
	if len(one.Fields) != 1 || one.Fields[0].Label != "MK4S-1" {
		t.Errorf("status mk4 answered %s", text(one))
	}

	none := ask(newBot(fleet, nil, nil), Request{Channel: bots, Text: "status nope"})
	if !strings.Contains(text(none), "The printers are: Mini, MK4S-1, XL") {
		t.Errorf("status nope answered %s", text(none))
	}
}

func TestStatusHere(t *testing.T) {
	chat := &fakeChat{}
	fleet := fakeFleet{printers: []Printer{{ID: "a", Name: "MK4S-1", Connected: true, State: "IDLE"}}}

	got := ask(newBot(fleet, chat, nil), Request{Channel: bots, Text: "status --here"})
	if text(got) != "\nPosted." || len(chat.posts) != 1 || chat.posts[0].Title != "Printers" {
		t.Errorf("answered %q and posted %+v", text(got), chat.posts)
	}
}

func TestJobs(t *testing.T) {
	fleet := fakeFleet{
		printers: []Printer{{ID: "a", Name: "MK4S-1"}, {ID: "b", Name: "XL"}},
		history: map[string][]Job{
			"a": {{ID: "1", File: "old.bgcode", Outcome: OutcomeFinished, Started: start.Add(-3 * time.Hour), Ended: start.Add(-2 * time.Hour)}},
			"b": {{ID: "2", File: "new.bgcode", Outcome: OutcomeFailed, Started: start.Add(-time.Hour), Ended: start.Add(-30 * time.Minute)}},
		},
	}

	got := ask(newBot(fleet, nil, nil), Request{Channel: bots, Text: "jobs"})
	if len(got.Lines) != 2 || !strings.HasPrefix(got.Lines[0], "❌ XL · new.bgcode · 30 min") ||
		!strings.HasPrefix(got.Lines[1], "✅ MK4S-1 · old.bgcode · 1 h 00 min") {
		t.Errorf("jobs answered %s", text(got))
	}
}

func TestSnap(t *testing.T) {
	fleet := fakeFleet{printers: []Printer{{ID: "a", Name: "MK4S-1"}, {ID: "b", Name: "XL"}, {ID: "c", Name: "Mini"}}}
	stills := fakeStills{cameras: map[string]error{"a": nil, "b": ErrCameraBusy}}

	cases := map[string]string{
		"snap":      "Which printer? These have a camera: MK4S-1, XL.",
		"snap mk4s": "Posted.",
		"snap xl":   "XL's camera has no recent picture",
		"snap mini": "Mini has no camera set up.",
	}

	for in, want := range cases {
		chat := &fakeChat{}

		got := ask(newBot(fleet, chat, stills), Request{Channel: bots, Text: in})
		if !strings.Contains(text(got), want) {
			t.Errorf("%s answered %q, want %q", in, text(got), want)
		}

		if want == "Posted." && (len(chat.uploads) != 1 || !strings.HasPrefix(chat.uploads[0], "MK4S-1 at ")) {
			t.Errorf("%s uploaded %v", in, chat.uploads)
		}
	}
}

// TestSnapNotInChannel says to invite the bot when Slack will not take the
// picture because the bot is not in the channel.
func TestSnapNotInChannel(t *testing.T) {
	fleet := fakeFleet{printers: []Printer{{ID: "a", Name: "MK4S-1"}}}
	stills := fakeStills{cameras: map[string]error{"a": nil}}
	chat := &fakeChat{err: fmt.Errorf("file upload: %w: not_in_channel", ErrNotInChannel)}

	got := ask(newBot(fleet, chat, stills), Request{Channel: bots, Text: "snap"})
	if !strings.Contains(text(got), "Invite it with /invite") {
		t.Errorf("snap answered %q", text(got))
	}
}

func TestSpan(t *testing.T) {
	cases := map[time.Duration]string{
		20 * time.Second:           "less than a minute",
		5 * time.Minute:            "5 min",
		26*time.Hour + time.Minute: "26 h 01 min",
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

func TestCombinedStills(t *testing.T) {
	pi := fakeStills{cameras: map[string]error{"a": nil, "b": ErrCameraBusy}}
	server := fakeStills{cameras: map[string]error{"b": nil, "c": nil}}
	both := CombinedStills{Pi: pi, Server: server}

	for _, device := range []string{"a", "b", "c"} {
		if !both.Has(device) {
			t.Errorf("no camera for %s", device)
		}

		if _, err := both.Still(context.Background(), device, true); err != nil {
			t.Errorf("%s: %v (a Pi that fails falls back to the server)", device, err)
		}
	}

	if both.Has("d") {
		t.Error("a camera for d")
	}

	if _, err := both.Still(context.Background(), "d", true); !errors.Is(err, ErrNoCamera) {
		t.Errorf("d: %v", err)
	}

	if _, err := (CombinedStills{}).Still(context.Background(), "a", true); !errors.Is(err, ErrNoCamera) {
		t.Errorf("no sources: %v", err)
	}
}
