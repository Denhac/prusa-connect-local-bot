package domain

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

// TestPasswordWords holds the list to lower-case words of 3 to 8 letters, each
// once, and enough of them for about 30 bits in three.
func TestPasswordWords(t *testing.T) {
	word := regexp.MustCompile(`^[a-z]{3,8}$`)
	seen := map[string]bool{}

	for _, w := range passwordWords {
		if !word.MatchString(w) {
			t.Errorf("%q is not 3 to 8 lower-case letters", w)
		}

		if seen[w] {
			t.Errorf("%q is listed twice", w)
		}

		seen[w] = true
	}

	if bits := 3 * math.Log2(float64(len(passwordWords))); bits < 30 {
		t.Errorf("three words from %d give %.1f bits, under 30", len(passwordWords), bits)
	}
}

// TestNewPassword is three words from the list, joined by hyphens, and the
// server's 8 to 72 bytes.
func TestNewPassword(t *testing.T) {
	seen := map[string]bool{}

	for range 50 {
		pw, err := newPassword()
		if err != nil {
			t.Fatal(err)
		}

		parts := strings.Split(pw, "-")
		if len(parts) != 3 || len(pw) < 8 || len(pw) > 72 {
			t.Fatalf("%q is not three words", pw)
		}

		for _, p := range parts {
			if !strings.Contains(" "+wordList+" ", p) {
				t.Errorf("%q is not from the list", p)
			}
		}

		seen[pw] = true
	}

	if len(seen) < 45 {
		t.Errorf("50 passwords gave only %d different ones", len(seen))
	}
}
