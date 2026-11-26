package domain

import (
	"fmt"
	"strings"
	"time"
)

// span says how long something took or will take, to the minute.
func span(d time.Duration) string {
	d = d.Round(time.Minute)

	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
	}

	return fmt.Sprintf("%d days", int(d.Hours())/24)
}

// since says how long ago a moment was.
func since(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}

	return span(now.Sub(then))
}

// clock is a moment as a person at the space reads it.
func clock(t time.Time, now time.Time) string {
	t = t.Local()
	if y, m, d := t.Date(); y == now.Local().Year() && m == now.Local().Month() && d == now.Local().Day() {
		return t.Format("15:04")
	}

	return t.Format("2 Jan 15:04")
}

// outcomeIcon and outcomeVerb describe how a print ended.
func outcomeIcon(outcome string) string {
	switch outcome {
	case OutcomeFinished:
		return "✅"
	case OutcomeFailed:
		return "❌"
	case OutcomeCancelled:
		return "⏹"
	}

	return "❔"
}

func outcomeVerb(outcome string) string {
	switch outcome {
	case OutcomeFinished:
		return "finished"
	case OutcomeFailed:
		return "failed"
	case OutcomeCancelled:
		return "cancelled"
	}

	return "ended"
}

// fileName is a print's file, or a stand-in when the printer gave none.
func fileName(j Job) string {
	if j.File != "" {
		return j.File
	}

	return "a print"
}

// took is how long an ended print ran: the printer's own count when it gave
// one, otherwise from start to end.
func took(j Job) time.Duration {
	if j.PrintedKnown {
		return j.Printed
	}

	if j.Ended.IsZero() || j.Started.IsZero() {
		return 0
	}

	return j.Ended.Sub(j.Started)
}

// temp reads a heater as "215/215 °C", or "25 °C" when it is off.
func temp(t Temp) string {
	if !t.Known {
		return "?"
	}

	if t.Target > 0 {
		return fmt.Sprintf("%.0f/%.0f °C", t.Now, t.Target)
	}

	return fmt.Sprintf("%.0f °C", t.Now)
}

// endLine is the reply that closes a print's thread.
func endLine(j Job) string {
	parts := []string{outcomeIcon(j.Outcome) + " " + capitalize(outcomeVerb(j.Outcome))}

	if d := took(j); d > 0 {
		if j.Outcome == OutcomeFinished {
			parts[0] += " in " + span(d)
		} else {
			parts[0] += " after " + span(d)
		}
	}

	if j.FilamentKnown && j.FilamentMM > 0 {
		parts = append(parts, fmt.Sprintf("%.1f m of filament", j.FilamentMM/1000))
	}

	return strings.Join(parts, " · ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
