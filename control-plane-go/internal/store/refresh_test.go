package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"infrapilot/internal/store"
)

func TestRefreshTokenRotationAndReuseDetection(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	u, _ := st.CreateUser(ctx, "r@x.co", "h", nil)

	if err := st.CreateRefreshToken(ctx, u.ID, "hash-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	st.CreateRefreshToken(ctx, u.ID, "hash-2", time.Hour)

	userID, err := st.ConsumeRefreshToken(ctx, "hash-1")
	if err != nil || userID != u.ID {
		t.Fatalf("consume: %q %v", userID, err)
	}
	// Using a consumed token again signals theft: every token of the user is revoked.
	if _, err := st.ConsumeRefreshToken(ctx, "hash-1"); !errors.Is(err, store.ErrTokenReused) {
		t.Fatalf("reuse err = %v", err)
	}
	if _, err := st.ConsumeRefreshToken(ctx, "hash-2"); err == nil {
		t.Fatal("other tokens must be revoked after reuse")
	}
	if _, err := st.ConsumeRefreshToken(ctx, "unknown"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown err = %v", err)
	}

	st.CreateRefreshToken(ctx, u.ID, "expired", -time.Minute)
	if _, err := st.ConsumeRefreshToken(ctx, "expired"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired err = %v", err)
	}
	st.CreateRefreshToken(ctx, u.ID, "hash-3", time.Hour)
	if err := st.RevokeRefreshToken(ctx, "hash-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsumeRefreshToken(ctx, "hash-3"); err == nil {
		t.Fatal("revoked token must not work")
	}
}
