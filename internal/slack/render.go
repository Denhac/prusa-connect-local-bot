// Package slack connects the bot to Slack: Socket Mode for commands, the Web
// API for posts, and Block Kit for drawing a domain.Message. It is the only
// package that imports github.com/slack-go/slack (internal/arch checks this).
package slack

import (
	"strings"

	"github.com/slack-go/slack"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

// fieldsPerSection is Slack's limit on fields in one section block.
const fieldsPerSection = 10

// Render draws a message as Block Kit blocks.
//
// Lines and the footer are plain_text, so nothing in them is read as markup.
// Field labels are bold, which needs mrkdwn; their values are escaped.
func Render(m domain.Message) []slack.Block {
	var blocks []slack.Block

	if m.Title != "" {
		blocks = append(blocks, slack.NewHeaderBlock(plain(m.Title)))
	}

	if len(m.Lines) > 0 {
		blocks = append(blocks, slack.NewSectionBlock(plain(strings.Join(m.Lines, "\n")), nil, nil))
	}

	for start := 0; start < len(m.Fields); start += fieldsPerSection {
		end := min(start+fieldsPerSection, len(m.Fields))
		fields := make([]*slack.TextBlockObject, 0, end-start)

		for _, f := range m.Fields[start:end] {
			fields = append(fields, slack.NewTextBlockObject(slack.MarkdownType,
				"*"+escape(f.Label)+"*\n"+escape(f.Value), false, false))
		}

		blocks = append(blocks, slack.NewSectionBlock(nil, fields, nil))
	}

	if m.Link != "" {
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject(slack.MarkdownType, "<"+escape(m.Link)+">", false, false), nil, nil))
	}

	if len(m.Buttons) > 0 {
		elements := make([]slack.BlockElement, 0, len(m.Buttons))

		for _, b := range m.Buttons {
			button := slack.NewButtonBlockElement(b.Action, b.Value, plain(b.Label))
			if b.Danger {
				button.Style = slack.StyleDanger
			} else {
				button.Style = slack.StylePrimary
			}

			elements = append(elements, button)
		}

		blocks = append(blocks, slack.NewActionBlock("", elements...))
	}

	if m.Footer != "" {
		blocks = append(blocks, slack.NewContextBlock("", plain(m.Footer)))
	}

	return blocks
}

func plain(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.PlainTextType, text, false, false)
}

// escape keeps text from becoming a link, a mention or a channel reference in
// mrkdwn, as Slack's formatting guide asks.
func escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
}
