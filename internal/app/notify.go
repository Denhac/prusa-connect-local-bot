package app

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
	"github.com/denhac/prusa-connect-local-bot/internal/store"
)

// The pace of the status-message loop.
const (
	tickEvery    = 5 * time.Second
	aliveEvery   = time.Minute
	historyLimit = 50
	// inQueue bounds the batches waiting for the loop. When it is full, a
	// batch is dropped and the fleet read again, rather than the event reader
	// blocking the server's stream.
	inQueue      = 64
	stillTimeout = 20 * time.Second
	sendAttempts = 3
)

// notifyLoop owns the reducer and the state file, and posts what the reducer
// decides. One goroutine runs it (docs/design.md, "Status to #bots").
type notifyLoop struct {
	log     *slog.Logger
	store   *store.Store
	state   store.Status
	reducer *domain.Notifier
	fleet   domain.Fleet
	events  domain.Events
	chat    domain.Chat
	stills  domain.Stills
	channel string
	version string
	now     func() time.Time

	in     chan []domain.Event
	resync atomic.Bool

	mu     sync.Mutex
	status domain.NotifyStatus
}

func newNotifyLoop(
	log *slog.Logger, s *store.Store, st store.Status, cfg domain.NotifyConfig,
	fleet domain.Fleet, events domain.Events, chat domain.Chat, stills domain.Stills,
	channel, version string, now func() time.Time,
) *notifyLoop {
	return &notifyLoop{
		log: log.With("component", "notify"), store: s, state: st,
		reducer: domain.NewNotifier(cfg, st.Notify),
		fleet:   fleet, events: events, chat: chat, stills: stills,
		channel: channel, version: version, now: now,
		in: make(chan []domain.Event, inQueue),
	}
}

// Status is how the loop is doing, for health.
func (l *notifyLoop) Status() domain.NotifyStatus {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.status
}

func (l *notifyLoop) setStatus(change func(*domain.NotifyStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()

	change(&l.status)
}

// run follows the event stream until ctx ends.
func (l *notifyLoop) run(ctx context.Context) {
	l.setStatus(func(s *domain.NotifyStatus) { s.Running, s.Detail = true, "" })
	defer l.setStatus(func(s *domain.NotifyStatus) { s.Running = false })

	l.announce(ctx)
	l.rebuild(ctx)

	reading := make(chan struct{})

	go func() {
		defer close(reading)

		_ = l.events.Run(ctx, l.receive) // Run logs its own trouble and ends with ctx.
	}()

	tick := time.NewTicker(tickEvery)
	alive := time.NewTicker(aliveEvery)

	defer tick.Stop()
	defer alive.Stop()

	for {
		select {
		case <-ctx.Done():
			<-reading
			l.save()

			return
		case batch := <-l.in:
			if l.resync.Swap(false) {
				l.rebuild(ctx)
			}

			l.apply(ctx, batch)
		case <-tick.C:
			if l.resync.Swap(false) {
				l.rebuild(ctx)
			}

			if actions := l.reducer.Tick(l.now()); len(actions) > 0 {
				l.execute(ctx, actions)
				l.save()
			}
		case <-alive.C:
			l.state.LastAlive = l.now()
			l.save()
		}
	}
}

// receive is called by the event reader. It never blocks the reader.
func (l *notifyLoop) receive(_ context.Context, batch []domain.Event, resync bool) {
	if resync {
		l.log.Info("the server says continuity is gone; reading the fleet again")
		l.resync.Store(true)
	}

	if len(batch) == 0 {
		return
	}

	select {
	case l.in <- batch:
	default:
		l.log.Warn("the status loop is behind; dropping a batch and reading the fleet again", "events", len(batch))
		l.resync.Store(true)
	}
}

func (l *notifyLoop) apply(ctx context.Context, batch []domain.Event) {
	now := l.now()

	for _, ev := range batch {
		l.execute(ctx, l.reducer.Apply(ev, now))

		if ev.At.After(l.state.LastApplied) {
			l.state.LastApplied = ev.At
		}
	}

	l.setStatus(func(s *domain.NotifyStatus) { s.LastEvent = now })
	l.save()
}

// announce posts that the module started, when that is news.
func (l *notifyLoop) announce(ctx context.Context) {
	now := l.now()

	if m, post := domain.StartMessage(l.version, l.state.Version, l.state.LastAlive, now); post {
		if _, err := l.chat.Post(ctx, l.channel, m); err != nil {
			l.log.Warn("could not post the start message", "error", err)
		}
	}

	l.state.Version, l.state.LastAlive = l.version, now
	l.save()
}

// rebuild reads the fleet and hands it to the reducer, which posts what
// changed while the bot was not reading (docs/design.md, "Rebuild").
func (l *notifyLoop) rebuild(ctx context.Context) {
	now := l.now()

	since := l.state.LastApplied
	if since.IsZero() {
		since = now // the first start reports no history
	}

	printers, err := l.fleet.Printers(ctx)
	if err != nil {
		l.log.Warn("could not read the fleet; trying again shortly", "error", err)
		l.setStatus(func(s *domain.NotifyStatus) { s.Detail = "cannot read the fleet" })
		l.resync.Store(true)

		return
	}

	snap := domain.Snapshot{Printers: printers, Current: map[string]*domain.Job{}, Ended: map[string][]domain.Job{}}

	for _, p := range printers {
		if current, _, err := l.fleet.Jobs(ctx, p.ID); err == nil && current != nil {
			snap.Current[p.ID] = current
		}

		if ended, err := l.fleet.History(ctx, p.ID, since, historyLimit); err == nil {
			snap.Ended[p.ID] = ended
		}
	}

	l.execute(ctx, l.reducer.Rebuild(snap, now))
	l.state.LastApplied = now
	l.setStatus(func(s *domain.NotifyStatus) { s.Detail = "" })
	l.save()
}

// execute posts the reducer's actions, keeping each message's id under its
// thread key, so a later reply finds its thread.
func (l *notifyLoop) execute(ctx context.Context, actions []domain.Action) {
	for _, a := range actions {
		thread, ok := l.state.Threads[a.Thread]

		switch {
		case a.Kind == domain.ActUpdate:
			if ok {
				l.attempt(ctx, "update", func() error { return l.chat.Update(ctx, thread.Channel, thread.TS, a.Message) })
			}

			continue
		case a.Kind == domain.ActReply && ok:
			l.attempt(ctx, "reply", func() error {
				_, err := l.chat.Reply(ctx, thread.Channel, thread.TS, a.Message)

				return err
			})
		default:
			// A post, or a reply whose thread was never posted: a message
			// of its own.
			var ts string

			sent := l.attempt(ctx, "post", func() error {
				var err error
				ts, err = l.chat.Post(ctx, l.channel, a.Message)

				return err
			})
			if !sent {
				continue
			}

			thread = store.Thread{Channel: l.channel, TS: ts}
			if a.Kind == domain.ActPost {
				l.state.Threads[a.Thread] = thread
			}
		}

		if a.StillOf != "" {
			l.still(ctx, a.StillOf, thread, a.Message.Plain())
		}
	}

	keys := make([]string, 0, len(l.state.Threads))
	for k := range l.state.Threads {
		keys = append(keys, k)
	}

	for _, k := range l.reducer.Forgotten(keys) {
		delete(l.state.Threads, k)
	}
}

// attempt tries a call a few times, so a short Slack outage loses nothing.
func (l *notifyLoop) attempt(ctx context.Context, what string, call func() error) bool {
	wait := 2 * time.Second

	for i := range sendAttempts {
		err := call()
		if err == nil {
			return true
		}

		if i == sendAttempts-1 || ctx.Err() != nil {
			l.log.Warn("could not post a status message", "action", what, "error", err)
			l.setStatus(func(s *domain.NotifyStatus) { s.Detail = "the last post failed" })

			return false
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(wait):
		}

		wait *= 2
	}

	return false
}

// still uploads the printer's camera picture into a thread, when it has a
// camera and the camera answers.
func (l *notifyLoop) still(ctx context.Context, device string, thread store.Thread, title string) {
	if l.stills == nil || !l.stills.Has(device) {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, stillTimeout)
	defer cancel()

	jpeg, err := l.stills.Still(ctx, device, false)
	if err != nil {
		l.log.Info("no still for a status message", "device", device, "error", err)

		return
	}

	if err := l.chat.Upload(ctx, thread.Channel, thread.TS, jpeg, title); err != nil {
		l.log.Warn("could not upload a still", "device", device, "error", err)
	}
}

func (l *notifyLoop) save() {
	l.state.Notify = l.reducer.State()

	if err := l.store.SaveStatus(l.state); err != nil {
		l.log.Error("could not save the state file", "error", err)
	}
}
