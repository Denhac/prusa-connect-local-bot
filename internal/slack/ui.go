package slack

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// maxTitle is Slack's limit on a modal's title.
const maxTitle = 24

// ui implements domain.UI for one command or one click.
type ui struct {
	api         *slack.Client
	http        *http.Client
	responseURL string
	trigger     string
	user        string
	// replace makes an answer replace the message a button was clicked in.
	replace bool
}

// Respond answers the person alone, through response_url. With none, as after
// a window's submit, it sends a direct message instead.
func (u ui) Respond(ctx context.Context, m domain.Message) error {
	if u.responseURL == "" {
		return u.DM(ctx, m)
	}

	err := slack.PostWebhookCustomHTTPContext(ctx, u.responseURL, u.http, &slack.WebhookMessage{
		ResponseType:    slack.ResponseTypeEphemeral,
		ReplaceOriginal: u.replace,
		Text:            m.Plain(),
		Blocks:          &slack.Blocks{BlockSet: Render(m)},
	})
	if err != nil {
		return fmt.Errorf("response_url: %w", err)
	}

	return nil
}

// OpenModal opens a window, which needs the trigger of the last few seconds.
func (u ui) OpenModal(ctx context.Context, m domain.Modal) (string, error) {
	if u.trigger == "" {
		return "", fmt.Errorf("views.open: no trigger")
	}

	resp, err := u.api.OpenViewContext(ctx, u.trigger, modalView(m))
	if err != nil {
		return "", fmt.Errorf("views.open: %w", err)
	}

	return resp.ID, nil
}

// UpdateModal replaces an open window. A window the person closed answers an
// error, and the caller then answers some other way.
func (u ui) UpdateModal(ctx context.Context, id string, m domain.Modal) error {
	if _, err := u.api.UpdateViewContext(ctx, modalView(m), "", "", id); err != nil {
		return fmt.Errorf("views.update: %w", err)
	}

	return nil
}

// DM sends the person a direct message.
func (u ui) DM(ctx context.Context, m domain.Message) error {
	channel, _, _, err := u.api.OpenConversationContext(ctx, &slack.OpenConversationParameters{Users: []string{u.user}})
	if err != nil {
		return fmt.Errorf("conversations.open: %w", err)
	}

	if _, _, err := u.api.PostMessageContext(ctx, channel.ID,
		slack.MsgOptionText(m.Plain(), false), slack.MsgOptionBlocks(Render(m)...)); err != nil {
		return fmt.Errorf("chat.postMessage: %w", err)
	}

	return nil
}

// modalView draws a window. Its blocks are drawn like a message's, and its
// input, if any, becomes an input block whose block and action ids are the
// input's id.
func modalView(m domain.Modal) slack.ModalViewRequest {
	title := m.Title
	if r := []rune(title); len(r) > maxTitle {
		title = string(r[:maxTitle])
	}

	blocks := Render(domain.Message{Lines: m.Lines, Fields: m.Fields})

	if in := m.Input; in != nil {
		element := slack.NewPlainTextInputBlockElement(nil, in.ID)
		element.InitialValue = in.Initial

		var hint *slack.TextBlockObject
		if in.Hint != "" {
			hint = plain(in.Hint)
		}

		blocks = append(blocks, slack.NewInputBlock(in.ID, plain(in.Label), hint, element))
	}

	view := slack.ModalViewRequest{
		Type:            slack.VTModal,
		Title:           plain(title),
		Close:           plain("Close"),
		Blocks:          slack.Blocks{BlockSet: blocks},
		CallbackID:      m.Callback,
		PrivateMetadata: m.Meta,
	}

	if m.Callback != "" {
		view.Submit = plain(m.Submit)
	}

	return view
}

// working is what a submitted window shows while the bot works.
func working() *slack.ModalViewRequest {
	v := modalView(domain.Modal{Title: "Working", Lines: []string{"Working on it…"}})

	return &v
}

// Profiles reads Slack users: their custom profile fields by label, and their
// names. It implements verify.Profiles and domain.Directory.
type Profiles struct {
	api *slack.Client
	now func() time.Time

	mu      sync.Mutex
	labels  map[string]string
	fetched time.Time
}

// labelsFor is how long the workspace's field labels are reused.
const labelsFor = time.Hour

// NewProfiles reads profiles with the bot token.
func NewProfiles(cfg Config, log *slog.Logger, now func() time.Time) *Profiles {
	options := []slack.Option{slack.OptionLog(slog.NewLogLogger(log.Handler(), slog.LevelDebug))}
	if cfg.APIURL != "" {
		options = append(options, slack.OptionAPIURL(cfg.APIURL))
	}

	return &Profiles{api: slack.New(cfg.BotToken.Reveal(), options...), now: now}
}

// Fields is a user's custom profile fields, by their labels
// (users.profile.get, with team.profile.get for the labels).
func (p *Profiles) Fields(ctx context.Context, user string) (map[string]string, error) {
	labels, err := p.fieldLabels(ctx)
	if err != nil {
		return nil, err
	}

	profile, err := p.api.GetUserProfileContext(ctx, &slack.GetUserProfileParameters{UserID: user})
	if err != nil {
		return nil, fmt.Errorf("users.profile.get: %w", err)
	}

	out := map[string]string{}

	for id, field := range profile.FieldsMap() {
		label := field.Label
		if label == "" {
			label = labels[id]
		}

		if label != "" {
			out[label] = strings.TrimSpace(field.Value)
		}
	}

	return out, nil
}

func (p *Profiles) fieldLabels(ctx context.Context) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.labels != nil && p.now().Sub(p.fetched) < labelsFor {
		return p.labels, nil
	}

	team, err := p.api.GetTeamProfileContext(ctx)
	if err != nil {
		if p.labels != nil {
			return p.labels, nil // the last labels are better than none
		}

		return nil, fmt.Errorf("team.profile.get: %w", err)
	}

	labels := make(map[string]string, len(team.Fields))
	for _, f := range team.Fields {
		labels[f.ID] = f.Label
	}

	p.labels, p.fetched = labels, p.now()

	return labels, nil
}

// Name is a user's display name, or their real name.
func (p *Profiles) Name(ctx context.Context, user string) (string, error) {
	info, err := p.api.GetUserInfoContext(ctx, user)
	if err != nil {
		return "", fmt.Errorf("users.info: %w", err)
	}

	for _, name := range []string{info.Profile.DisplayName, info.Profile.RealName, info.Name} {
		if name != "" {
			return name, nil
		}
	}

	return user, nil
}
