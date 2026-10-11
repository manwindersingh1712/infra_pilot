package api

import "time"

// StreamConfig tunes the log stream routes. Zero values mean the defaults.
type StreamConfig struct {
	// Burst is the per-IP burst of the limiter shared by the stream-ticket and
	// stream routes (one token per second). Default 20.
	Burst int
}

const (
	defaultStreamBurst = 20
	streamEvery        = time.Second // 60 requests/min per client address
)
