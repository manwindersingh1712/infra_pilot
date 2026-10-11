package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

// ErrTokenReused means a refresh token was presented after it was consumed:
// someone else may hold a copy, so all of the user's tokens were revoked.
var ErrTokenReused = errors.New("refresh token reused")

// CreateRefreshToken stores the SHA-256 hash of a refresh token. Plain tokens are never stored.
func (s *Store) CreateRefreshToken(ctx context.Context, userID, tokenHash string, ttl time.Duration) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO "RefreshToken" ("id", "userId", "tokenHash", "expiresAt")
		 VALUES ($1, $2, $3, now() + $4::int * interval '1 second')`,
		cuid.New(), userID, tokenHash, int(ttl.Seconds()))
	return err
}

// ConsumeRefreshToken revokes a valid token and returns its user (rotation).
// A token that was already used revokes every token of its user.
func (s *Store) ConsumeRefreshToken(ctx context.Context, tokenHash string) (string, error) {
	var userID string
	err := s.db.QueryRow(ctx,
		`UPDATE "RefreshToken" SET "revokedAt" = now()
		 WHERE "tokenHash" = $1 AND "revokedAt" IS NULL AND "expiresAt" > now()
		 RETURNING "userId"`, tokenHash).Scan(&userID)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var revoked bool
	err = s.db.QueryRow(ctx,
		`SELECT "userId", "revokedAt" IS NOT NULL FROM "RefreshToken" WHERE "tokenHash" = $1`, tokenHash).
		Scan(&userID, &revoked)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !revoked) {
		return "", ErrNotFound // unknown or expired
	}
	if err != nil {
		return "", err
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE "RefreshToken" SET "revokedAt" = now() WHERE "userId" = $1 AND "revokedAt" IS NULL`, userID); err != nil {
		return "", err
	}
	return "", ErrTokenReused
}

func (s *Store) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "RefreshToken" SET "revokedAt" = now() WHERE "tokenHash" = $1 AND "revokedAt" IS NULL`, tokenHash)
	return err
}
