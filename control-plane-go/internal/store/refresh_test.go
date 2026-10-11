package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"infrapilot/internal/store"
)

// newUser creates a user and returns its id.
func newUser(t *testing.T, st *store.Store, email string) string {
	t.Helper()
	u, err := st.CreateUser(context.Background(), email, "h", nil)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func mkToken(t *testing.T, st *store.Store, userID, hash string) {
	t.Helper()
	if err := st.CreateRefreshToken(context.Background(), userID, hash, time.Hour); err != nil {
		t.Fatal(err)
	}
}

// familyOf returns the family id of a stored token.
func familyOf(t *testing.T, hash string) string {
	t.Helper()
	var fam string
	if err := pool.QueryRow(context.Background(),
		`SELECT "familyId" FROM "RefreshToken" WHERE "tokenHash" = $1`, hash).Scan(&fam); err != nil {
		t.Fatal(err)
	}
	return fam
}

func liveInFamily(t *testing.T, fam string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM "RefreshToken" WHERE "familyId" = $1 AND "revokedAt" IS NULL AND "expiresAt" > now()`,
		fam).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// backdate makes a rotated token look like it was rotated `secs` seconds ago.
func backdate(t *testing.T, hash string, secs int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE "RefreshToken" SET "rotatedAt" = now() - $2::int * interval '1 second' WHERE "tokenHash" = $1`,
		hash, secs); err != nil {
		t.Fatal(err)
	}
}

func rotate(st *store.Store, oldHash, newHash string) (string, error) {
	return st.RotateRefreshToken(context.Background(), oldHash, newHash, time.Hour)
}

func TestRotateRefreshTokenBasics(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	uid := newUser(t, st, "r@x.co")
	mkToken(t, st, uid, "hash-1")

	got, err := rotate(st, "hash-1", "hash-2")
	if err != nil || got != uid {
		t.Fatalf("rotate: %q %v", got, err)
	}
	if familyOf(t, "hash-1") != familyOf(t, "hash-2") {
		t.Fatal("child must inherit the parent's family")
	}
	// A replay inside the grace window is a benign race: not found, no revoke.
	if _, err := rotate(st, "hash-1", "hash-x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replay in grace err = %v", err)
	}
	if _, err := rotate(st, "hash-2", "hash-3"); err != nil {
		t.Fatalf("child must still rotate: %v", err)
	}
	if _, err := rotate(st, "unknown", "hash-y"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown err = %v", err)
	}

	// Expired and not revoked: not found, and no revoke (expiry is not theft).
	if err := st.CreateRefreshToken(ctx, uid, "expired", -time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "expired", "hash-z"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired err = %v", err)
	}
	if n := liveInFamily(t, familyOf(t, "hash-3")); n != 1 {
		t.Fatalf("live tokens in the family = %d, want 1", n)
	}
}

func TestNewTokensStartTheirOwnFamily(t *testing.T) {
	st := newStore(t)
	uid := newUser(t, st, "fam@x.co")
	mkToken(t, st, uid, "a")
	mkToken(t, st, uid, "b")
	if familyOf(t, "a") == familyOf(t, "b") || familyOf(t, "a") == "" {
		t.Fatal("each CreateRefreshToken must start a distinct, non-empty family")
	}
}

// (a) Racing rotations of one token: exactly one winner, no theft verdict.
func TestRotateRefreshTokenConcurrent(t *testing.T) {
	for _, workers := range []int{2, 5} {
		t.Run(fmt.Sprintf("%d goroutines", workers), func(t *testing.T) {
			st := newStore(t)
			uid := newUser(t, st, "c@x.co")
			for rep := range 20 {
				old := fmt.Sprintf("old-%d", rep)
				mkToken(t, st, uid, old)
				other := fmt.Sprintf("other-%d", rep)
				mkToken(t, st, uid, other) // a second session of the same user

				start := make(chan struct{})
				errs := make([]error, workers)
				var wg sync.WaitGroup
				for i := range workers {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						_, errs[i] = rotate(st, old, fmt.Sprintf("new-%d-%d", rep, i))
					}()
				}
				close(start)
				wg.Wait()

				winner := -1
				for i, err := range errs {
					switch {
					case err == nil:
						if winner != -1 {
							t.Fatalf("rep %d: two winners", rep)
						}
						winner = i
					case errors.Is(err, store.ErrNotFound):
					default:
						t.Fatalf("rep %d: goroutine %d err = %v (want nil or ErrNotFound)", rep, i, err)
					}
				}
				if winner == -1 {
					t.Fatalf("rep %d: no winner: %v", rep, errs)
				}
				newHash := fmt.Sprintf("new-%d-%d", rep, winner)
				if n := liveInFamily(t, familyOf(t, old)); n != 1 {
					t.Fatalf("rep %d: %d live tokens in the family, want 1", rep, n)
				}
				if _, err := rotate(st, newHash, fmt.Sprintf("next-%d", rep)); err != nil {
					t.Fatalf("rep %d: winner's token must rotate: %v", rep, err)
				}
				if _, err := rotate(st, other, fmt.Sprintf("other-next-%d", rep)); err != nil {
					t.Fatalf("rep %d: second session was disturbed: %v", rep, err)
				}
			}
		})
	}
}

// (b) A failed child INSERT rolls everything back: the old token stays usable.
func TestRotateRefreshTokenIsAtomic(t *testing.T) {
	st := newStore(t)
	uid := newUser(t, st, "atomic@x.co")
	mkToken(t, st, uid, "old")
	mkToken(t, st, uid, "taken")

	_, err := rotate(st, "old", "taken") // child hash collides with an existing token
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTokenReused) {
		t.Fatalf("err = %v, want a database error", err)
	}
	if n := liveInFamily(t, familyOf(t, "old")); n != 1 {
		t.Fatalf("old token not live after a failed rotation (%d live)", n)
	}
	if _, err := rotate(st, "old", "fresh"); err != nil {
		t.Fatalf("old token must still rotate: %v", err)
	}
}

// (c) Replay after the grace window is theft: the whole family dies, other families live.
func TestRotateRefreshTokenReuseAfterGrace(t *testing.T) {
	st := newStore(t)
	uid := newUser(t, st, "reuse@x.co")
	mkToken(t, st, uid, "p")
	mkToken(t, st, uid, "other-session")
	if _, err := rotate(st, "p", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "c", "gc"); err != nil {
		t.Fatal(err)
	}
	fam := familyOf(t, "p")

	// Inside the window (5 s): plain not-found, nothing revoked.
	backdate(t, "p", 5)
	if _, err := rotate(st, "p", "x1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replay inside grace err = %v", err)
	}
	if n := liveInFamily(t, fam); n != 1 {
		t.Fatalf("grace replay revoked tokens: %d live, want 1", n)
	}

	// Outside the window (11 s): theft.
	backdate(t, "p", 11)
	if _, err := rotate(st, "p", "x2"); !errors.Is(err, store.ErrTokenReused) {
		t.Fatalf("replay after grace err = %v", err)
	}
	if n := liveInFamily(t, fam); n != 0 {
		t.Fatalf("%d live tokens left in the compromised family", n)
	}
	if _, err := rotate(st, "gc", "x3"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("descendant err = %v, want ErrNotFound", err)
	}
	if _, err := rotate(st, "other-session", "ok"); err != nil {
		t.Fatalf("a second family of the same user must survive: %v", err)
	}
}

// (d) SEC-1: a replay-after-grace of the parent runs while the child is being
// rotated. Whatever the order, the family ends with no live token.
func TestRotateRefreshTokenReplayRacesChildRotation(t *testing.T) {
	st := newStore(t)
	uid := newUser(t, st, "race@x.co")
	for rep := range 20 {
		p, c, gc := fmt.Sprintf("p-%d", rep), fmt.Sprintf("c-%d", rep), fmt.Sprintf("gc-%d", rep)
		mkToken(t, st, uid, p)
		if _, err := rotate(st, p, c); err != nil {
			t.Fatal(err)
		}
		backdate(t, p, 11)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var replayErr, childErr error
		run := func(f func()) {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; f() }()
		}
		replay := func() { _, replayErr = rotate(st, p, "evil-"+p) }
		child := func() { _, childErr = rotate(st, c, gc) }
		if rep%2 == 0 { // alternate which goroutine is scheduled first
			run(replay)
			run(child)
		} else {
			run(child)
			run(replay)
		}
		close(start)
		wg.Wait()

		if !errors.Is(replayErr, store.ErrTokenReused) {
			t.Fatalf("rep %d: replay err = %v", rep, replayErr)
		}
		if childErr != nil && !errors.Is(childErr, store.ErrNotFound) {
			t.Fatalf("rep %d: child err = %v", rep, childErr)
		}
		if n := liveInFamily(t, familyOf(t, p)); n != 0 {
			t.Fatalf("rep %d: %d live tokens survived the family revoke (child err %v)", rep, n, childErr)
		}
	}
}

// (e) Logout revokes the presenter's family only.
func TestRevokeRefreshFamily(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	uid := newUser(t, st, "lo@x.co")
	mkToken(t, st, uid, "a")
	mkToken(t, st, uid, "b")

	if err := st.RevokeRefreshFamily(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "a", "a2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("logged-out token err = %v, want ErrNotFound (no revoke)", err)
	}
	if _, err := rotate(st, "b", "b2"); err != nil {
		t.Fatalf("other family disturbed: %v", err)
	}

	// Logout with a stale (rotated) token kills the live descendant.
	if _, err := rotate(st, "b2", "b3"); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeRefreshFamily(ctx, "b2"); err != nil {
		t.Fatal(err)
	}
	if n := liveInFamily(t, familyOf(t, "b3")); n != 0 {
		t.Fatalf("descendant still live after stale-token logout (%d)", n)
	}
	// Unknown token and repeat are no-ops.
	for _, h := range []string{"nope", "a", "b2"} {
		if err := st.RevokeRefreshFamily(ctx, h); err != nil {
			t.Fatalf("revoke %q: %v", h, err)
		}
	}
}

// Old (pre-00004) revoked rows have no rotatedAt: a replay is a logout-style
// revocation and never triggers a revoke.
func TestRevokedWithoutRotatedAtDoesNotRevokeFamily(t *testing.T) {
	st := newStore(t)
	uid := newUser(t, st, "old@x.co")
	mkToken(t, st, uid, "x")
	if _, err := pool.Exec(context.Background(),
		`UPDATE "RefreshToken" SET "revokedAt" = now() - interval '1 hour' WHERE "tokenHash" = 'x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := rotate(st, "x", "y"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
