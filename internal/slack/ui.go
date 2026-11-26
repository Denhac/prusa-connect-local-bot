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
	// gate orders the answers to a window's submit after its
	// acknowledgement. Nil for a command or a click.
	gate *gate
	log  *slog.Logger
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

// refuse answers a refusal: in the window, for a window's submit, so it does
// not stay at "Working on it…", and as an answer otherwise.
func (u ui) refuse(ctx context.Context, m domain.Message) {
	if u.gate != nil {
		_ = u.UpdateModal(ctx, u.gate.id, domain.Modal{Title: "Printer bot", Lines: m.Lines})

		return
	}

	_ = u.Respond(ctx, m)
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
	view := modalView(m)

	if u.gate != nil && id == u.gate.id {
		if u.gate.offer(view) {
			return nil // sent in the acknowledgement
		}

		if err := u.gate.wait(ctx); err != nil {
			return fmt.Errorf("views.update: %w", err)
		}
	}

	if _, err := u.api.UpdateViewContext(ctx, view, "", "", id); err != nil {
		if u.log != nil {
			u.log.Warn("could not update a window", "error", err)
		}

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

	blocks := Render(domain.Message{Lines: m.Lines, Fields: m.Fields, Link: m.Link})

	for _, in := range m.Inputs {
		var hint *slack.TextBlockObject
		if in.Hint != "" {
			hint = plain(in.Hint)
		}

		block := slack.NewInputBlock(in.ID, plain(in.Label), hint, inputElement(in))
		block.Optional = in.Optional
		blocks = append(blocks, block)
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

// Slack's limits on choices: checkboxes take at most 10, a menu 100, and an
// option's text and description 75 characters each.
const (
	maxCheckboxes  = 10
	maxMenuOptions = 100
	maxOptionText  = 75
)

// inputElement is a field's element: a line of text, checkboxes, or for more
// choices than checkboxes take, a menu that picks several.
func inputElement(in domain.Input) slack.BlockElement {
	if len(in.Choices) == 0 {
		element := slack.NewPlainTextInputBlockElement(nil, in.ID)
		element.InitialValue = in.Initial

		return element
	}

	choices := in.Choices
	if len(choices) > maxMenuOptions {
		choices = choices[:maxMenuOptions]
	}

	options := make([]*slack.OptionBlockObject, 0, len(choices))

	for _, c := range choices {
		var detail *slack.TextBlockObject
		if c.Detail != "" && len(in.Choices) <= maxCheckboxes {
			detail = plain(cut(c.Detail, maxOptionText))
		}

		options = append(options, slack.NewOptionBlockObject(c.Value, plain(cut(c.Label, maxOptionText)), detail))
	}

	if len(in.Choices) <= maxCheckboxes {
		return slack.NewCheckboxGroupsBlockElement(in.ID, options...)
	}

	return slack.NewOptionsMultiSelectBlockElement(slack.MultiOptTypeStatic, plain("Pick one or more"), in.ID, options...)
}

func cut(text string, most int) string {
	if r := []rune(text); len(r) > most {
		return string(r[:most-1]) + "…"
	}

	return text
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
