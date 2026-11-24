package wslc

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var offsetInTime = regexp.MustCompile(`[+-]\d{4}`)

// ParseTime accepts the layouts wslc prints. The zone name after a numeric
// offset (for example "GMT+8") is optional and is ignored when the standard
// layout does not consume it.
func ParseTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("wslc: empty time")
	}
	layouts := []string{
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.000000000 -0700 MST",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05.000000000 -0700",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	if loc := offsetInTime.FindStringIndex(value); loc != nil {
		trimmed := strings.TrimSpace(value[:loc[1]])
		for _, layout := range []string{
			"2006-01-02 15:04:05 -0700",
			"2006-01-02 15:04:05.000000000 -0700",
		} {
			if t, err := time.Parse(layout, trimmed); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("wslc: unrecognized time %q", value)
}

// NormalizeSince converts a Docker since/until value into an integer Unix
// second or an RFC3339 timestamp, which is what wslc accepts.
func NormalizeSince(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return value, nil
	}
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return value, nil
	}
	if _, err := strconv.ParseInt(value, 10, 64); err == nil {
		return value, nil
	}
	if f, err := strconv.ParseFloat(value, 64); err == nil {
		return strconv.FormatInt(int64(f), 10), nil
	}
	return "", fmt.Errorf("wslc: invalid since/until %q", value)
}
