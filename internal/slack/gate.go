package slack

import (
	"context"
	"sync"
	"time"

	"github.com/slack-go/slack"
)

// A window's submit is acknowledged with the view the window shows next, and
// the bot's answer then replaces that view through views.update. Slack applies
// the acknowledgement when it gets it, so an answer ready within milliseconds
// could land first and be overwritten by "Working on it…", which then stays.
//
// gate puts the two in order. An answer the handler has before the
// acknowledgement goes out is sent in it. One that comes later waits
// settleDelay after the acknowledgement before it is sent.
type gate struct {
	id string // the submitted window

	mu      sync.Mutex
	view    *slack.ModalViewRequest
	decided bool
	at      time.Time
	ready   chan struct{} // closed when an answer is offered in time
	settled chan struct{} // closed when the acknowledgement goes out
}

// How long the acknowledgement waits for an answer (Slack allows 3 s), and
// how long a later answer waits after it.
const (
	ackWait     = 2 * time.Second
	settleDelay = time.Second
)

func newGate(id string) *gate {
	return &gate{id: id, ready: make(chan struct{}), settled: make(chan struct{})}
}

// offer hands the first answer to the acknowledgement, and reports false when
// the acknowledgement has gone already.
func (g *gate) offer(v slack.ModalViewRequest) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.decided {
		return false
	}

	g.view, g.decided = &v, true
	close(g.ready)

	return true
}

// settle closes the gate when the acknowledgement goes out, and answers the
// view it carries: the offered answer, or nil for "Working on it…".
func (g *gate) settle(now time.Time) *slack.ModalViewRequest {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.at.IsZero() {
		return g.view // settled already
	}

	g.decided, g.at = true, now
	close(g.settled)

	return g.view
}

// wait holds a later answer until settleDelay after the acknowledgement,
// waiting first for the acknowledgement itself.
func (g *gate) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller's own deadline.
	case <-g.settled:
	}

	g.mu.Lock()
	until := g.at.Add(settleDelay)
	g.mu.Unlock()

	d := time.Until(until)
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // the caller's own deadline.
	case <-timer.C:
		return nil
	}
}

// acknowledge waits up to ackWait for an answer, and answers the view the
// acknowledgement carries.
func (g *gate) acknowledge(now func() time.Time) slack.ModalViewRequest {
	timer := time.NewTimer(ackWait)
	defer timer.Stop()

	select {
	case <-g.ready:
	case <-timer.C:
	}

	if v := g.settle(now()); v != nil {
		return *v
	}

	return *working()
}
