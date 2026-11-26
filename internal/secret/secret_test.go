package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

const value = "xoxb-not-a-real-token"

func TestNothingPrintsTheValue(t *testing.T) {
	s := New(value)

	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "token", s)
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "token", s)

	encoded, err := json.Marshal(struct{ Token Secret }{s})
	if err != nil {
		t.Fatal(err)
	}

	printed := []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s),
		fmt.Sprintf("%v", struct{ Token Secret }{s}), s.String(), s.GoString(),
		logged.String(), string(encoded),
	}
	for _, out := range printed {
		if strings.Contains(out, value) {
			t.Errorf("the value was printed: %s", out)
		}
	}

	if s.Reveal() != value {
		t.Errorf("Reveal answered %q", s.Reveal())
	}
}

func TestUnmarshal(t *testing.T) {
	var held struct {
		Token Secret `json:"token"`
	}

	if err := json.Unmarshal([]byte(`{"token":"`+value+`"}`), &held); err != nil {
		t.Fatal(err)
	}

	if held.Token.Reveal() != value {
		t.Errorf("read %q", held.Token.Reveal())
	}

	if err := json.Unmarshal([]byte(`{"token":12}`), &held); err == nil {
		t.Error("a number was taken as a secret")
	}
}

func TestEmpty(t *testing.T) {
	if !(Secret{}).Empty() || (Secret{}).String() != "" {
		t.Error("the zero secret is not empty")
	}
}
