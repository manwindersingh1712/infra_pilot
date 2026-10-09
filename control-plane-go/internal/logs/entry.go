// Package logs stores container logs in two tiers: Redis (last 10 minutes,
// plus pub/sub for live viewers) and ClickHouse (queryable history, 30 days).
package logs

import "time"

// Entry is one log line. The JSON shape is what the UI and the Redis pub/sub
// channel have always used.
type Entry struct {
	DeploymentID string `json:"deploymentId"`
	Source       string `json:"source"` // stdout | stderr | system
	Message      string `json:"message"`
	Timestamp    string `json:"timestamp"`
}

// FormatTime renders t like JavaScript's Date.toISOString().
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// ParseTime reads a FormatTime value, falling back to now for malformed input.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}
