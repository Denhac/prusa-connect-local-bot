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
	"github.com/denhac/prusa-connect-local-bot/internal/verify"
)

// The defaults module.yaml also declares, used when a value arrives empty.
const (
	DefaultServerURL     = "http://127.0.0.1:8080"
	DefaultAdminUsername = "slackbot"
)

// ErrInvalid is a value the module cannot use. The message names the key,
// because the operator reads it on the instance.
var ErrInvalid = errors.New("settings: invalid value")

// Sent is what the host sends. The JSON names are the keys in module.yaml.
type Sent struct {
	SlackBotToken secret.Secret     `json:"slack_bot_token"`
	SlackAppToken secret.Secret     `json:"slack_app_token"`
	StatusChannel string            `json:"status_channel"`
	AllowGuests   bool              `json:"allow_guests"`
	Cameras       map[string]string `json:"cameras"`
	IssueReasons  []string          `json:"issue_reasons"`
	ServerURL     string            `json:"server_url"`
	AdminUsername string            `json:"admin_username"`
	AdminPassword secret.Secret     `json:"admin_password"`
	PublicURL     string            `json:"public_url"`
	Verify        *verify.Config    `json:"verify"`
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
	// Cameras is each printer's Pi camera still address, by printer id.
	// internal/camera checks the addresses when the module starts.
	Cameras map[string]string
	// IssueReasons are the server's issue reasons that mean a printer needs
	// a person.
	IssueReasons []string
	// ServerURL is where the bot signs in to the server's HTTP API.
	// internal/serverhttp checks it when the module starts.
	ServerURL string
	// AdminUsername and AdminPassword are the bot's own account. With no
	// password, everything that needs a sign-in stays off.
	AdminUsername string
	AdminPassword secret.Secret
	// PublicURL is where people open the printer server, for the link page.
	PublicURL string
	// Verify is where the membership facts are.
	Verify verify.Config
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

	for device, address := range sent.Cameras {
		if device == "" || address == "" {
			return Values{}, fmt.Errorf("%w: cameras maps a printer id to a still address, "+
				"such as {\"<printer id>\": \"http://<Pi LAN address>:8555/v1/cameras/cam0/still\"}", ErrInvalid)
		}
	}

	for _, reason := range sent.IssueReasons {
		if reason == "" || strings.ContainsAny(reason, " \t") {
			return Values{}, fmt.Errorf("%w: issue_reasons holds %q, and a reason is one word such as awaiting_operator",
				ErrInvalid, reason)
		}
	}

	if sent.ServerURL == "" {
		sent.ServerURL = DefaultServerURL
	}

	if sent.AdminUsername == "" {
		sent.AdminUsername = DefaultAdminUsername
	}

	verifyConfig := verify.Defaults()
	if sent.Verify != nil {
		verifyConfig = mergeVerify(verifyConfig, *sent.Verify)
	}

	return Values{
		SlackBotToken: sent.SlackBotToken, SlackAppToken: sent.SlackAppToken, StatusChannel: sent.StatusChannel,
		AllowGuests: sent.AllowGuests, Cameras: sent.Cameras, IssueReasons: sent.IssueReasons,
		ServerURL: sent.ServerURL, AdminUsername: sent.AdminUsername, AdminPassword: sent.AdminPassword,
		PublicURL: strings.TrimRight(sent.PublicURL, "/"), Verify: verifyConfig,
	}, nil
}

// mergeVerify keeps a default for each key left empty.
func mergeVerify(base, over verify.Config) verify.Config {
	for _, pair := range []struct {
		to   *string
		from string
	}{
		{&base.MemberField, over.MemberField}, {&base.CodeField, over.CodeField},
		{&base.MemberURL, over.MemberURL}, {&base.Authorization, over.Authorization},
	} {
		if pair.from != "" {
			*pair.to = pair.from
		}
	}

	return base
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
