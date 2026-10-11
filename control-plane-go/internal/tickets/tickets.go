// Package tickets issues short-lived, single-use tickets stored in Redis.
package tickets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrInvalid = errors.New("invalid or expired ticket")

const prefix = "tickets:"

type Store struct {
	c   *redis.Client
	ttl time.Duration
}

func New(url string, ttl time.Duration) (*Store, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("ticket ttl must be positive, got %v", ttl)
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Store{c: redis.NewClient(opt), ttl: ttl}, nil
}

func (s *Store) Close() error                   { return s.c.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.c.Ping(ctx).Err() }

// Issue stores value under a new random ticket (256 bits) for the TTL.
func (s *Store) Issue(ctx context.Context, value string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("issue ticket: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(buf)
	if err := s.c.Set(ctx, prefix+ticket, value, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("issue ticket: %w", err)
	}
	return ticket, nil
}

// Redeem returns the ticket's value and deletes it atomically (GETDEL), so a
// ticket works exactly once.
func (s *Store) Redeem(ctx context.Context, ticket string) (string, error) {
	if !wellFormed(ticket) {
		return "", ErrInvalid
	}
	v, err := s.c.GetDel(ctx, prefix+ticket).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrInvalid
	}
	if err != nil {
		return "", fmt.Errorf("redeem ticket: %w", err)
	}
	return v, nil
}

// ticketLen is the length of 32 random bytes in unpadded base64 (RawURL).
const ticketLen = 43

// wellFormed reports whether ticket has exactly the form Issue produces, so
// malformed input never reaches Redis.
func wellFormed(ticket string) bool {
	if len(ticket) != ticketLen {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(ticket)
	return err == nil && len(b) == 32
}
