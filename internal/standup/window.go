// Package standup collects one identity's recent work for a daily standup:
// GitHub activity, the sprint board and vault notes. It only reads; the
// /ki-recap skill turns the report into prose.
package standup

import (
	"errors"
	"fmt"
	"time"
)

// ErrBadSince is returned for a --since value that is not a past date.
var ErrBadSince = errors.New("invalid --since")

// Since returns the start of the standup window: 00:00 local on the previous
// working day, so Monday starts on Friday. A YYYY-MM-DD flag overrides it.
func Since(now time.Time, flag string) (time.Time, error) {
	if flag != "" {
		t, err := time.ParseInLocation(time.DateOnly, flag, now.Location())
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: %q is not YYYY-MM-DD", ErrBadSince, flag)
		}
		if t.After(now) {
			return time.Time{}, fmt.Errorf("%w: %s is in the future", ErrBadSince, flag)
		}
		return t, nil
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
	for day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		day = day.AddDate(0, 0, -1)
	}
	return day, nil
}
