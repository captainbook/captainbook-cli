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
		compFrom := fromDate.AddDate(-1, 0, 0)
		compTo := toDate.AddDate(-1, 0, 0)
		return compFrom.Format(dateFormat), compTo.Format(dateFormat), nil

	default:
		return "", "", fmt.Errorf("unknown comparison shorthand %q (use \"previous\" or \"year-ago\")", shorthand)
	}
}
