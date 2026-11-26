// Package secret holds a credential so that it is not printed by accident.
//
// A Secret prints as [redacted] through fmt, log/slog and encoding/json. The
// only way to read the value is Reveal, so every place a credential leaves the
// process can be found by searching for that one name.
package secret

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// Redacted is what a Secret prints as.
const Redacted = "[redacted]"

// Secret is a credential. The zero value is an empty secret.
type Secret struct {
	value string
}

// New wraps a value.
func New(value string) Secret { return Secret{value: value} }

// Reveal returns the value. Call it only where the credential is sent.
func (s Secret) Reveal() string { return s.value }

// Empty reports whether no value is held.
func (s Secret) Empty() bool { return s.value == "" }

// String prints [redacted], or nothing for an empty secret.
func (s Secret) String() string {
	if s.Empty() {
		return ""
	}

	return Redacted
}

// GoString keeps %#v from printing the value.
func (s Secret) GoString() string { return "secret.Secret{" + s.String() + "}" }

// Format makes every fmt verb print what String prints.
func (s Secret) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, s.String()) }

// LogValue makes log/slog print what String prints.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalJSON writes what String prints, so a status document carries no value.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String()) //nolint:wrapcheck // a string always encodes.
}

// UnmarshalJSON reads a JSON string, which is how the host sends a secret setting.
func (s *Secret) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("a secret is a JSON string: %w", err)
	}

	s.value = value

	return nil
}
