package compare

import (
	"fmt"
	"time"
)

const dateFormat = "2006-01-02"

// Resolve calculates comparison dates from a shorthand value.
// Supported shorthands: "previous" and "year-ago".
func Resolve(shorthand, from, to string) (compareFrom, compareTo string, err error) {
	fromDate, err := time.Parse(dateFormat, from)
	if err != nil {
		return "", "", fmt.Errorf("invalid from date %q: %w", from, err)
	}
	toDate, err := time.Parse(dateFormat, to)
	if err != nil {
		return "", "", fmt.Errorf("invalid to date %q: %w", to, err)
	}

	switch shorthand {
	case "previous":
		// The comparison period is the same duration, immediately before the main period.
		duration := toDate.Sub(fromDate)
		compTo := fromDate.AddDate(0, 0, -1)
		compFrom := compTo.Add(-duration)
		return compFrom.Format(dateFormat), compTo.Format(dateFormat), nil

	case "year-ago":
		compFrom := addYearClamped(fromDate, -1)
		compTo := addYearClamped(toDate, -1)
		return compFrom.Format(dateFormat), compTo.Format(dateFormat), nil

	default:
		return "", "", fmt.Errorf("unknown comparison shorthand %q (use \"previous\" or \"year-ago\")", shorthand)
	}
}

// addYearClamped adds years to a date, clamping to the last day of the target
// month when the day overflows (e.g. Feb 29 → Feb 28 in a non-leap year).
func addYearClamped(t time.Time, years int) time.Time {
	y, m, d := t.Date()
	targetYear := y + years
	// Find last day of target month
	lastDay := time.Date(targetYear, m+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(targetYear, m, d, 0, 0, 0, 0, t.Location())
}
