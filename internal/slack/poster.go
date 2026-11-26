package slack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// spacing is the least time between two messages to one channel. Slack allows
// about one a second.
const spacing = 1100 * time.Millisecond

// Poster implements domain.Chat over the Web API.
type Poster struct {
	api *slack.Client
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	next map[string]time.Time
}

// NewPoster builds the poster with the bot token.
func NewPoster(cfg Config, log *slog.Logger, now func() time.Time) *Poster {
	options := []slack.Option{slack.OptionLog(slog.NewLogLogger(log.Handler(), slog.LevelDebug))}
	if cfg.APIURL != "" {
		options = append(options, slack.OptionAPIURL(cfg.APIURL))
	}

	return &Poster{
		api:  slack.New(cfg.BotToken.Reveal(), options...),
		log:  log.With("component", "slack"),
		now:  now,
		next: map[string]time.Time{},
	}
}

// Post starts a message in a channel.
func (p *Poster) Post(ctx context.Context, channel string, m domain.Message) (string, error) {
	return p.send(ctx, channel, "", m)
}

// Reply answers in a thread.
func (p *Poster) Reply(ctx context.Context, channel, thread string, m domain.Message) (string, error) {
	return p.send(ctx, channel, thread, m)
}

func (p *Poster) send(ctx context.Context, channel, thread string, m domain.Message) (string, error) {
	options := []slack.MsgOption{
		slack.MsgOptionText(m.Plain(), false),
		slack.MsgOptionBlocks(Render(m)...),
	}
	if thread != "" {
		options = append(options, slack.MsgOptionTS(thread))
	}

	var ts string

	err := p.paced(ctx, channel, func() error {
		var err error
		_, ts, err = p.api.PostMessageContext(ctx, channel, options...)

		return err //nolint:wrapcheck // wrapped once, below.
	})
	if err != nil {
		return "", fmt.Errorf("chat.postMessage: %w", err)
	}

	return ts, nil
}

// Update replaces a message.
func (p *Poster) Update(ctx context.Context, channel, id string, m domain.Message) error {
	err := p.paced(ctx, channel, func() error {
		_, _, _, err := p.api.UpdateMessageContext(ctx, channel, id,
			slack.MsgOptionText(m.Plain(), false), slack.MsgOptionBlocks(Render(m)...))

		return err //nolint:wrapcheck // wrapped once, below.
	})
	if err != nil {
		return fmt.Errorf("chat.update: %w", err)
	}

	return nil
}

// Upload shares a JPEG, through files.getUploadURLExternal and
// files.completeUploadExternal (files.upload is retired).
func (p *Poster) Upload(ctx context.Context, channel, thread string, jpeg []byte, title string) error {
	err := p.paced(ctx, channel, func() error {
		_, err := p.api.UploadFileContext(ctx, slack.UploadFileParameters{
			Reader:          bytes.NewReader(jpeg),
			FileSize:        len(jpeg),
			Filename:        "still.jpg",
			Title:           title,
			AltTxt:          title,
			Channel:         channel,
			ThreadTimestamp: thread,
		})

		return err //nolint:wrapcheck // wrapped once, below.
	})
	if err != nil {
		return fmt.Errorf("file upload: %w", err)
	}

	return nil
}

// paced runs one call no sooner than spacing after the last call to the same
// channel, and once more after Slack's own wait when Slack says to slow down.
func (p *Poster) paced(ctx context.Context, channel string, call func() error) error {
	for attempt := 0; ; attempt++ {
		if err := p.wait(ctx, channel); err != nil {
			return err
		}

		err := call()

		var limited *slack.RateLimitedError
		if !errors.As(err, &limited) || attempt > 0 {
			return err
		}

		p.log.Warn("Slack asked the bot to slow down", "channel", channel, "retry_after", limited.RetryAfter)

		p.mu.Lock()
		p.next[channel] = p.now().Add(limited.RetryAfter)
		p.mu.Unlock()
	}
}

func (p *Poster) wait(ctx context.Context, channel string) error {
	p.mu.Lock()
	now := p.now()

	at := now
	if next := p.next[channel]; next.After(now) {
		at = next
	}

	p.next[channel] = at.Add(spacing)
	p.mu.Unlock()

	if d := at.Sub(now); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // the caller's own cancellation.
		case <-time.After(d):
		}
	}

	return nil
}
