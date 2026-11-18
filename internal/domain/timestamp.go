package domain

import (
	"fmt"
	"time"
)

// TimestampLayout is the only timestamp layout the persisted format uses. It is
// fixed-width on purpose: lease expiry is compared as TEXT, and variable-width
// RFC3339 orders incorrectly ("...:00Z" sorts after "...:00.5Z").
const TimestampLayout = "2006-01-02T15:04:05.000000000Z"

// FormatTimestamp renders an instant in the persisted format.
func FormatTimestamp(t time.Time) string { return t.UTC().Format(TimestampLayout) }

// ParseTimestamp reads the persisted format back.
func ParseTimestamp(s string) (time.Time, error) {
	parsed, err := time.Parse(TimestampLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q is not in the persisted layout: %w", s, err)
	}
	return parsed.UTC(), nil
}
