package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/store"
)

// chatLog records every call, as "kind channel/thread: text".
type chatLog struct {
	mu    sync.Mutex
	calls []string
	n     int
}

func (c *chatLog) add(format string, args ...any) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.n++
	c.calls = append(c.calls, fmt.Sprintf(format, args...))

	return fmt.Sprintf("ts%d", c.n)
}

func (c *chatLog) Post(_ context.Context, channel string, m domain.Message) (string, error) {
	return c.add("post %s: %s", channel, m.Plain()), nil
}

func (c *chatLog) Reply(_ context.Context, channel, thread string, m domain.Message) (string, error) {
	return c.add("reply %s/%s: %s", channel, thread, m.Plain()), nil
}

func (c *chatLog) Update(_ context.Context, channel, id string, m domain.Message) error {
	c.add("update %s/%s: %s", channel, id, m.Plain())

	return nil
}

func (c *chatLog) Upload(_ context.Context, channel, thread string, _ []byte, _ string) error {
	c.add("upload %s/%s", channel, thread)

	return nil
}

func (c *chatLog) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.calls, "\n")
}

type oneCamera struct{}

func (oneCamera) Has(device string) bool { return device == "a" }

func (oneCamera) Still(context.Context, string, bool) ([]byte, error) { return []byte{0xff, 0xd8}, nil }

// printing is a fleet with one printer and, optionally, a print on its bed or
// just ended.
type printing struct {
	current *domain.Job
	ended   []domain.Job
}

func (p printing) Printers(context.Context) ([]domain.Printer, error) {
	return []domain.Printer{{ID: "a", Name: "MK4S-1", Connected: true}}, nil
}

func (p printing) Jobs(context.Context, string) (*domain.Job, *domain.Job, error) {
	return p.current, nil, nil
}

func (p printing) History(context.Context, string, time.Time, int) ([]domain.Job, error) {
	return p.ended, nil
}

var at = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func loopOver(t *testing.T, dir string, fleet domain.Fleet, chat domain.Chat) *notifyLoop {
	t.Helper()

	s, st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	return newNotifyLoop(slog.New(slog.DiscardHandler), s, st.Status, domain.DefaultNotifyConfig(),
		fleet, noEvents{}, chat, oneCamera{}, "C0BOTS", "0.2.0", func() time.Time { return at })
}

// TestAPrintThroughTheLoop posts a start, then threads the end and its still
// under that start, then restarts and posts nothing again.
func TestAPrintThroughTheLoop(t *testing.T) {
	dir := t.TempDir()
	chat := &chatLog{}
	l := loopOver(t, dir, printing{}, chat)
	ctx := context.Background()

	l.announce(ctx)
	l.rebuild(ctx)

	started := domain.Job{ID: "j1", File: "bracket.bgcode", Started: at}
	ended := started
	ended.Outcome, ended.Ended = domain.OutcomeFinished, at.Add(time.Hour)

	l.apply(ctx, []domain.Event{{Kind: domain.EventJobStarted, Device: "a", Job: started, At: at}})
	l.apply(ctx, []domain.Event{{Kind: domain.EventJobSettled, Device: "a", Job: ended, At: at.Add(time.Hour)}})

	want := "post C0BOTS: 🤖 Printer bot 0.2.0 started.\n" +
		"post C0BOTS: ▶ MK4S-1 started bracket.bgcode\n" +
		"reply C0BOTS/ts2: ✅ Finished in 1 h 00 min\n" +
		"upload C0BOTS/ts2\n" +
		"update C0BOTS/ts2: ✅ MK4S-1 finished bracket.bgcode"
	if got := chat.String(); got != want {
		t.Fatalf("posted\n%s\nwant\n%s", got, want)
	}

	// A restart within a minute, with the print already in the history.
	again := &chatLog{}
	l2 := loopOver(t, dir, printing{ended: []domain.Job{ended}}, again)
	l2.announce(ctx)
	l2.rebuild(ctx)
	l2.apply(ctx, []domain.Event{{Kind: domain.EventJobSettled, Device: "a", Job: ended, At: at.Add(time.Hour)}})

	if got := again.String(); got != "" {
		t.Errorf("a restart posted\n%s", got)
	}
}

// TestAReplyWithNoThreadIsPosted is a thread whose message the state file
// lost: the reply goes out as a message of its own.
func TestAReplyWithNoThreadIsPosted(t *testing.T) {
	chat := &chatLog{}
	l := loopOver(t, t.TempDir(), printing{}, chat)

	l.execute(context.Background(), []domain.Action{
		{Kind: domain.ActReply, Thread: "job/missing", Message: domain.Message{Lines: []string{"🔌 back"}}},
		{Kind: domain.ActUpdate, Thread: "job/missing", Message: domain.Message{Lines: []string{"x"}}},
	})

	if got := chat.String(); got != "post C0BOTS: 🔌 back" {
		t.Errorf("posted\n%s", got)
	}
}

func TestReceiveNeverBlocks(t *testing.T) {
	l := loopOver(t, t.TempDir(), printing{}, &chatLog{})

	for range inQueue + 5 {
		l.receive(context.Background(), []domain.Event{{Kind: domain.EventRecordChanged, Device: "a"}}, false)
	}

	if !l.resync.Load() {
		t.Error("a full queue did not ask for a resync")
	}
}
