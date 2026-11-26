// Package settings reads the values an operator sets for this module.
//
// The host sends them once, on plugin.configure, as one JSON object: the
// defaults in module.yaml with the operator's values written over them. A key
// whose default is empty and that nobody set is left out. manifest_test.go at
// the repository root holds this file and module.yaml to the same keys.
//
// Every setting is optional, because a required one that is missing makes the
// server refuse the install for good. The module runs idle instead and lists
// what is missing (see Missing).
package settings

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

// ErrInvalid is a value the module cannot use. The message names the key,
// because the operator reads it on the instance.
var ErrInvalid = errors.New("settings: invalid value")

// Sent is what the host sends. The JSON names are the keys in module.yaml.
type Sent struct {
	SlackBotToken secret.Secret `json:"slack_bot_token"`
	SlackAppToken secret.Secret `json:"slack_app_token"`
	StatusChannel string        `json:"status_channel"`
	AllowGuests   bool          `json:"allow_guests"`
}

// Values are the settings once checked.
type Values struct {
	// SlackBotToken is the bot token, xoxb-…, for the Web API.
	SlackBotToken secret.Secret
	// SlackAppToken is the app-level token, xapp-…, for Socket Mode.
	SlackAppToken secret.Secret
	// StatusChannel is the ID of the channel status goes to (#bots).
	StatusChannel string
	// AllowGuests lets Slack guest accounts use the command.
	AllowGuests bool
}

// channelID is the shape of a Slack channel ID: C for a channel, G for an old
// private channel.
var channelID = regexp.MustCompile(`^[CG][A-Z0-9]{6,}$`)

// Read checks what the host sent. An empty value is allowed everywhere; a
// value of the wrong shape is refused, so a token pasted into the wrong field
// is caught at once.
func Read(sent Sent) (Values, error) {
	if err := prefixed("slack_bot_token", sent.SlackBotToken, "xoxb-"); err != nil {
		return Values{}, err
	}

	if err := prefixed("slack_app_token", sent.SlackAppToken, "xapp-"); err != nil {
		return Values{}, err
	}

	if sent.StatusChannel != "" && !channelID.MatchString(sent.StatusChannel) {
		return Values{}, fmt.Errorf("%w: status_channel is %q, and a channel ID looks like C0123ABCD. "+
			"It is at the bottom of the channel's details in Slack", ErrInvalid, sent.StatusChannel)
	}

	return Values(sent), nil
}

// prefixed refuses a token that does not start as its kind does. The message
// never quotes the token.
func prefixed(key string, token secret.Secret, prefix string) error {
	if token.Empty() || strings.HasPrefix(token.Reveal(), prefix) {
		return nil
	}

	return fmt.Errorf("%w: %s does not start with %s. Check that the two Slack tokens are not swapped",
		ErrInvalid, key, prefix)
}

// Missing names the settings the module needs and does not have, in the order
// an operator sets them.
func (v Values) Missing() []string {
	var missing []string

	if v.SlackBotToken.Empty() {
		missing = append(missing, "slack_bot_token")
	}

	if v.SlackAppToken.Empty() {
		missing = append(missing, "slack_app_token")
	}

	if v.StatusChannel == "" {
		missing = append(missing, "status_channel")
	}

	return missing
}

// SlackReady reports whether the module has what it needs to connect to Slack.
func (v Values) SlackReady() bool {
	return !v.SlackBotToken.Empty() && !v.SlackAppToken.Empty()
}
