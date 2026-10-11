package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"

	"infrapilot/internal/auth"
)

// ErrTokenReused means a rotated refresh token was presented after the grace
// window: someone else may hold a copy, so its whole family was revoked.
var ErrTokenReused = errors.New("refresh token reused")

// ErrReplayInGrace is ErrNotFound (errors.Is matches both) for a token that
// was rotated within auth.RefreshGrace; callers may log it differently.
var ErrReplayInGrace = fmt.Errorf("%w: replayed inside the grace window", ErrNotFound)

// CreateRefreshToken stores the SHA-256 hash of a refresh token as the first
// token of a new family (one login). Plain tokens are never stored.
func (s *Store) CreateRefreshToken(ctx context.Context, userID, tokenHash string, ttl time.Duration) error {
	id := cuid.New()
	_, err := s.db.Exec(ctx,
		`INSERT INTO "RefreshToken" ("id", "userId", "familyId", "tokenHash", "expiresAt")
		 VALUES ($1, $2, $1, $3, now() + $4::int * interval '1 second')`,
		id, userID, tokenHash, int(ttl.Seconds()))
	return err
}

// lockFamily serialises every rotation and revoke of one family for the rest
// of the transaction, so a revoke cannot miss a child a concurrent rotation
// inserts after the revoke's snapshot.
func lockFamily(ctx context.Context, tx pgx.Tx, familyID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, familyID)
	return err
}

func revokeFamily(ctx context.Context, tx pgx.Tx, familyID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE "RefreshToken" SET "revokedAt" = now() WHERE "familyId" = $1 AND "revokedAt" IS NULL`, familyID)
	return err
}

// RotateRefreshToken consumes oldHash and stores newHash as its child (same
// family), all in one transaction, and returns the token's user.
//
//   - live token: rotated, user returned.
//   - unknown, expired, logged out, or rotated within auth.RefreshGrace:
//     ErrNotFound, nothing is revoked.
//   - rotated longer than auth.RefreshGrace ago: theft. The family is revoked
//     and the revoke is committed; ErrTokenReused is returned.
//
// Any other failure (including the child insert) rolls everything back, so the
// old token stays usable.
func (s *Store) RotateRefreshToken(ctx context.Context, oldHash, newHash string, ttl time.Duration) (string, error) {
	var userID string
	var reused bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var familyID string
		if err := tx.QueryRow(ctx,
			`SELECT "familyId" FROM "RefreshToken" WHERE "tokenHash" = $1`, oldHash).Scan(&familyID); err != nil {
			return noRows(err)
		}
		if err := lockFamily(ctx, tx, familyID); err != nil {
			return err
		}
		// A fresh statement after the lock sees whatever the previous lock holder committed.
		var live, rotated, withinGrace bool
		var id string
		if err := tx.QueryRow(ctx,
			`SELECT "id", "userId",
			        "revokedAt" IS NULL AND "expiresAt" > now(),
			        "rotatedAt" IS NOT NULL,
			        "rotatedAt" IS NOT NULL AND now() - "rotatedAt" <= $2::int * interval '1 second'
			   FROM "RefreshToken" WHERE "tokenHash" = $1 FOR UPDATE`,
			oldHash, int(auth.RefreshGrace.Seconds())).Scan(&id, &userID, &live, &rotated, &withinGrace); err != nil {
			return noRows(err)
		}
		switch {
		case live:
			if _, err := tx.Exec(ctx,
				`UPDATE "RefreshToken" SET "revokedAt" = now(), "rotatedAt" = now() WHERE "id" = $1`, id); err != nil {
				return err
			}
			_, err := tx.Exec(ctx,
				`INSERT INTO "RefreshToken" ("id", "userId", "familyId", "tokenHash", "expiresAt")
				 VALUES ($1, $2, $3, $4, now() + $5::int * interval '1 second')`,
				cuid.New(), userID, familyID, newHash, int(ttl.Seconds()))
			return err
		case rotated && !withinGrace:
			reused = true
			return revokeFamily(ctx, tx, familyID) // committed below although the call fails
		case rotated:
			return ErrReplayInGrace
		default: // expired, logged out, or an earlier family revoke
			return ErrNotFound
		}
	})
	if reused && err == nil {
		return "", ErrTokenReused
	}
	if err != nil {
		return "", err
	}
	return userID, nil
}

// RevokeRefreshFamily revokes every live token of the presented token's family
// (logout). The token may already be rotated or revoked. An unknown token is
// not an error.
func (s *Store) RevokeRefreshFamily(ctx context.Context, tokenHash string) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var familyID string
		if err := tx.QueryRow(ctx,
			`SELECT "familyId" FROM "RefreshToken" WHERE "tokenHash" = $1`, tokenHash).Scan(&familyID); err != nil {
			return err
		}
		if err := lockFamily(ctx, tx, familyID); err != nil {
			return err
		}
		return revokeFamily(ctx, tx, familyID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
