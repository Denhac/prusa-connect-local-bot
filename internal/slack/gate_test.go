package slack

import (
	"context"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

// TestGateSendsAQuickAnswerInTheAcknowledgement carries an answer offered
// before the acknowledgement in the acknowledgement itself.
func TestGateSendsAQuickAnswerInTheAcknowledgement(t *testing.T) {
	g := newGate("V1")

	if !g.offer(slack.ModalViewRequest{CallbackID: "answer"}) {
		t.Fatal("the first answer was not taken")
	}

	start := time.Now()
	if v := g.acknowledge(time.Now); v.CallbackID != "answer" {
		t.Errorf("the acknowledgement carries %+v", v)
	}

	if time.Since(start) > ackWait/2 {
		t.Errorf("the acknowledgement waited %v for an answer it had", time.Since(start))
	}

	if g.offer(slack.ModalViewRequest{CallbackID: "second"}) {
		t.Error("a second answer was taken for the acknowledgement")
	}
}

// TestGateHoldsALaterAnswer acknowledges with "Working on it…" when no answer
// comes in time, and holds a later one until after the acknowledgement.
func TestGateHoldsALaterAnswer(t *testing.T) {
	g := newGate("V1")

	waited := make(chan time.Duration, 1)

	go func() {
		time.Sleep(10 * time.Millisecond) // before the acknowledgement, after the gate is open

		if g.offer(slack.ModalViewRequest{}) {
			waited <- -1

			return
		}

		begin := time.Now()
		_ = g.wait(context.Background())
		waited <- time.Since(begin)
	}()

	// Settle at once, as if ackWait had passed with no answer.
	if v := g.settle(time.Now()); v != nil {
		// The goroutine may have offered first; then the ack carries it.
		t.Skip("the answer came first, which TestGateSendsAQuickAnswerInTheAcknowledgement covers")
	}

	if d := <-waited; d >= 0 && d < settleDelay-50*time.Millisecond {
		t.Errorf("the later answer waited %v, under %v", d, settleDelay)
	}
}

// TestGateWaitWaitsForTheAcknowledgement holds an answer that comes while
// the acknowledgement is still being decided.
func TestGateWaitWaitsForTheAcknowledgement(t *testing.T) {
	g := newGate("V1")
	g.offer(slack.ModalViewRequest{}) // the first answer, for the acknowledgement

	done := make(chan struct{})

	go func() {
		_ = g.wait(context.Background())
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("a second answer went out before the acknowledgement")
	case <-time.After(50 * time.Millisecond):
	}

	g.settle(time.Now())

	select {
	case <-done:
	case <-time.After(settleDelay + time.Second):
		t.Fatal("the second answer never went out")
	}
}
