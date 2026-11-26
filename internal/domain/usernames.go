package domain

import (
	"strconv"
	"strings"
	"unicode"
)

// maxUsername is the longest username the bot makes.
const maxUsername = 32

// NormalizeUsername turns a name into a username: lower case, letters and
// digits, with '.', '_' and '-' kept and a space becoming '.', at most 32
// characters. It may come out empty.
func NormalizeUsername(name string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteRune('.')
		}
	}

	out := strings.Trim(b.String(), "._-")
	for strings.Contains(out, "..") {
		out = strings.ReplaceAll(out, "..", ".")
	}

	if len(out) > maxUsername {
		out = strings.TrimRight(out[:maxUsername], "._-")
	}

	return out
}

// ValidUsername is a username a person may type: already in the bot's shape.
func ValidUsername(name string) bool {
	return name != "" && NormalizeUsername(name) == name
}

// Reserved is a name the bot never gives out.
func Reserved(name, botAccount string) bool {
	return strings.HasPrefix(name, "admin") || name == "root" || name == "slackbot" ||
		strings.EqualFold(name, botAccount)
}

// NextFree is the first of base-2, base-3, … that taken does not hold,
// shortening base so the result fits.
func NextFree(base string, taken func(string) bool) string {
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)

		stem := base
		if len(stem)+len(suffix) > maxUsername {
			stem = strings.TrimRight(stem[:maxUsername-len(suffix)], "._-")
		}

		if candidate := stem + suffix; !taken(candidate) {
			return candidate
		}
	}
}
