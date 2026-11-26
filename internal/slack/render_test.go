package slack

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denhac/prusa-connect-local-bot/internal/domain"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata/golden")

// golden compares blocks with testdata/golden/<name>.json. Run
// `go test ./internal/slack -update` after an intended change, and review the
// diff of the JSON.
func golden(t *testing.T, name string, m domain.Message) {
	t.Helper()

	got, err := json.MarshalIndent(Render(m), "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", name+".json")

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}

	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from its golden file. Run with -update and review the diff.\n%s", name, got)
	}
}

func TestRenderHelp(t *testing.T) {
	golden(t, "help", domain.Help(true))
	golden(t, "help-member", domain.Help(false))
}

func TestRenderFields(t *testing.T) {
	golden(t, "fields", domain.Message{
		Title: "Printer bot",
		Fields: []domain.Field{
			{Label: "Version", Value: "0.1.0 (SDK v0.1.0-beta.2)"},
			{Label: "Slack", Value: "connected for 5 min"},
		},
		Footer: "Built on Prusa Connect by Prusa Research.",
	})
}

// TestRenderEscapes keeps text from a printer or a person from turning into a
// mention, a link or markup. Lines go out as plain_text, which Slack never
// reads as markup; field values go out as mrkdwn, so they are escaped.
func TestRenderEscapes(t *testing.T) {
	encoded, err := json.Marshal(Render(domain.Message{
		Lines:  []string{"<!channel> look"},
		Fields: []domain.Field{{Label: "a<b", Value: "<https://x.example|click> & more"}},
	}))
	if err != nil {
		t.Fatal(err)
	}

	var blocks []struct {
		Text *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"text"`
		Fields []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(encoded, &blocks); err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 2 || blocks[0].Text == nil || blocks[1].Fields == nil {
		t.Fatalf("drew %s", encoded)
	}

	if line := blocks[0].Text; line.Type != "plain_text" || line.Text != "<!channel> look" {
		t.Errorf("the line went out as %s %q", line.Type, line.Text)
	}

	if field := blocks[1].Fields[0].Text; field != "*a&lt;b*\n&lt;https://x.example|click&gt; &amp; more" {
		t.Errorf("the field went out as %q", field)
	}

	if strings.Contains(string(encoded), "mrkdwn") && strings.Contains(blocks[1].Fields[0].Text, "<https://") {
		t.Error("a link survived in mrkdwn")
	}
}

func TestRenderManyFields(t *testing.T) {
	fields := make([]domain.Field, 12)
	for i := range fields {
		fields[i] = domain.Field{Label: "L", Value: "V"}
	}

	if n := len(Render(domain.Message{Fields: fields})); n != 2 {
		t.Errorf("12 fields drew %d sections, want 2 (Slack takes at most 10 a section)", n)
	}
}
