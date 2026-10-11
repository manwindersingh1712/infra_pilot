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
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(buf)
	if err := s.c.Set(ctx, prefix+ticket, value, s.ttl).Err(); err != nil {
		return "", err
	}
	return ticket, nil
}

// Redeem returns the ticket's value and deletes it atomically (GETDEL), so a
// ticket works exactly once.
func (s *Store) Redeem(ctx context.Context, ticket string) (string, error) {
	v, err := s.c.GetDel(ctx, prefix+ticket).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrInvalid
	}
	return v, err
}
