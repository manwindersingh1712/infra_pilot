package tickets_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/tickets"
)

func TestNewRejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -1, -time.Second} {
		if s, err := tickets.New("redis://127.0.0.1:1", ttl); err == nil {
			_ = s.Close()
			t.Fatalf("New ttl=%v: want error", ttl)
		}
	}
}

func TestRedisErrorsAreWrappedNotInvalid(t *testing.T) {
	s, err := tickets.New("redis://127.0.0.1:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = s.Redeem(ctx, strings.Repeat("A", 43))
	if err == nil || errors.Is(err, tickets.ErrInvalid) || !strings.Contains(err.Error(), "redeem ticket") {
		t.Fatalf("redeem err = %v", err)
	}
	_, err = s.Issue(ctx, "v")
	if err == nil || errors.Is(err, tickets.ErrInvalid) || !strings.Contains(err.Error(), "issue ticket") {
		t.Fatalf("issue err = %v", err)
	}
}

func TestRedeemRejectsMalformedWithoutRedis(t *testing.T) {
	// Unreachable Redis: a Redis call would return a dial error, not ErrInvalid.
	s, err := tickets.New("redis://127.0.0.1:1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for name, tk := range map[string]string{
		"empty":    "",
		"short":    strings.Repeat("A", 42),
		"long":     strings.Repeat("A", 44),
		"huge":     strings.Repeat("A", 1<<20),
		"bad char": strings.Repeat("A", 42) + "=",
		"colon":    strings.Repeat("A", 42) + ":",
		"space":    strings.Repeat("A", 42) + " ",
	} {
		if _, err := s.Redeem(ctx, tk); !errors.Is(err, tickets.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
