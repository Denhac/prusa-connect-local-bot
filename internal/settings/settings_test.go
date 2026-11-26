package settings

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/denhac/prusa-connect-local-bot/internal/secret"
)

func TestRead(t *testing.T) {
	cases := map[string]struct {
		sent    Sent
		refused string // a word the refusal names, or empty when the values pass
	}{
		"nothing set":     {sent: Sent{}},
		"all set":         {sent: Sent{SlackBotToken: secret.New("xoxb-1"), SlackAppToken: secret.New("xapp-1"), StatusChannel: "C0123ABCD"}},
		"private channel": {sent: Sent{StatusChannel: "G0123ABCD"}},
		"swapped tokens":  {sent: Sent{SlackBotToken: secret.New("xapp-1")}, refused: "slack_bot_token"},
		"bad app token":   {sent: Sent{SlackAppToken: secret.New("xoxb-1")}, refused: "slack_app_token"},
		"channel name":    {sent: Sent{StatusChannel: "#bots"}, refused: "status_channel"},
		"monthly sweep":   {sent: Sent{SweepInterval: "720h", DeleteAfter: "0"}},
		"sweep off":       {sent: Sent{SweepInterval: "0"}},
		"sweep too often": {sent: Sent{SweepInterval: "10m"}, refused: "sweep_interval"},
		"sweep in days":   {sent: Sent{SweepInterval: "1d"}, refused: "sweep_interval"},
		"delete at once":  {sent: Sent{DeleteAfter: "1s"}, refused: "delete_after"},
		"no limit":        {sent: Sent{SweepMaxChanges: count(-1)}, refused: "sweep_max_changes"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Read(c.sent)

			switch {
			case c.refused == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.refused != "" && err == nil:
				t.Fatal("taken")
			case c.refused != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.refused)):
				t.Fatalf("the refusal %q does not name %s", err, c.refused)
			}
		})
	}
}

func TestRefusalNeverQuotesAToken(t *testing.T) {
	_, err := Read(Sent{SlackBotToken: secret.New("xapp-very-secret")})
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("the refusal quotes the token: %v", err)
	}
}

func TestMissing(t *testing.T) {
	values, err := Read(Sent{SlackAppToken: secret.New("xapp-1")})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := values.Missing(), []string{"slack_bot_token", "status_channel"}; !slices.Equal(got, want) {
		t.Errorf("Missing is %v, want %v", got, want)
	}

	if values.SlackReady() {
		t.Error("ready with one token")
	}
}

// TestDecodesWhatTheHostSends reads a configure payload in the shape the host
// writes it: text for secrets and strings, a JSON boolean for a bool, and
// unset keys left out.
func TestDecodesWhatTheHostSends(t *testing.T) {
	var sent Sent

	if err := json.Unmarshal([]byte(`{"slack_bot_token":"xoxb-1","allow_guests":false}`), &sent); err != nil {
		t.Fatal(err)
	}

	if sent.SlackBotToken.Reveal() != "xoxb-1" || sent.AllowGuests || sent.StatusChannel != "" {
		t.Errorf("decoded %+v", sent)
	}
}

func count(n int) *Count { c := Count(n); return &c }

// TestSweepSettings reads the defaults, and an int sent as a number or as text.
func TestSweepSettings(t *testing.T) {
	values, err := Read(Sent{})
	if err != nil || values.SweepInterval != 24*time.Hour || values.SweepMaxChanges != 5 || values.DeleteAfter != 90*24*time.Hour {
		t.Fatalf("defaults: %+v, %v", values, err)
	}

	for _, payload := range []string{`{"sweep_max_changes":0}`, `{"sweep_max_changes":"0"}`} {
		var sent Sent
		if err := json.Unmarshal([]byte(payload), &sent); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}

		if values, err := Read(sent); err != nil || values.SweepMaxChanges != 0 {
			t.Errorf("%s read as %d, %v", payload, values.SweepMaxChanges, err)
		}
	}

	var sent Sent
	if err := json.Unmarshal([]byte(`{"sweep_max_changes":"five"}`), &sent); err == nil {
		t.Error("five was taken for a number")
	}
}
