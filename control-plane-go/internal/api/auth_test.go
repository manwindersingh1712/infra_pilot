package api

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"infrapilot/internal/auth"
)

type authResp struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
	User         struct {
		ID    string  `json:"id"`
		Email string  `json:"email"`
		Name  *string `json:"name"`
	} `json:"user"`
}

const invalidRefreshBody = `{"error":"invalid_refresh_token"}`

func (hs *harness) register(email string) authResp {
	hs.t.Helper()
	rec := hs.do("POST", "/auth/register", "", map[string]any{"email": email, "password": "password123"})
	expectStatus(hs.t, rec, 200)
	return decode[authResp](hs.t, rec)
}

// hsOK fails the test unless rec is a 200 and returns it.
func hsOK(t *testing.T, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	expectStatus(t, rec, 200)
	return rec
}

func (hs *harness) refresh(tok string) *httptest.ResponseRecorder {
	hs.t.Helper()
	return hs.do("POST", "/auth/refresh", "", map[string]any{"refreshToken": tok})
}

func TestRegisterAndLogin(t *testing.T) {
	hs := newHarness(t)
	rec := hs.do("POST", "/auth/register", "", map[string]any{"email": "a@b.co", "password": "password123", "name": "A"})
	expectStatus(t, rec, 200)
	reg := decode[authResp](t, rec)
	if sub, err := hs.signer.Verify(reg.Token); err != nil || sub != reg.User.ID || reg.User.Email != "a@b.co" || reg.RefreshToken == "" {
		t.Fatalf("register response %+v (sub %q, err %v)", reg, sub, err)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("register response mentions the password: %s", rec.Body.String())
	}

	rec = hs.do("POST", "/auth/register", "", map[string]any{"email": "a@b.co", "password": "password123"})
	expectStatus(t, rec, 409)
	expectBody(t, rec, `{"error":"email_already_exists"}`)
	expectStatus(t, hs.do("POST", "/auth/register", "", map[string]any{"email": "a2@b.co", "password": "short"}), 400)
	expectStatus(t, hs.do("POST", "/auth/register", "", map[string]any{"email": "nope", "password": "password123"}), 400)

	rec = hs.do("POST", "/auth/login", "", map[string]any{"email": "a@b.co", "password": "password123"})
	expectStatus(t, rec, 200)
	if decode[authResp](t, rec).User.ID != reg.User.ID {
		t.Fatal("login returned a different user")
	}
	for _, body := range []map[string]any{
		{"email": "a@b.co", "password": "wrong-password"},
		{"email": "missing@b.co", "password": "password123"},
	} {
		rec = hs.do("POST", "/auth/login", "", body)
		expectStatus(t, rec, 401)
		expectBody(t, rec, `{"error":"invalid_credentials"}`)
	}
}

// STAFF-2: bcrypt reads only 72 bytes, so longer passwords are a client error
// (400), not a 500 and not a silent truncation. The limit is in bytes.
func TestRegisterPasswordLengthLimit(t *testing.T) {
	hs := newHarness(t)
	ok := strings.Repeat("a", 72)
	expectStatus(t, hs.do("POST", "/auth/register", "", map[string]any{"email": "p72@b.co", "password": ok}), 200)
	for name, pw := range map[string]string{
		"73 ascii bytes":       strings.Repeat("a", 73),
		"75 bytes in 25 runes": strings.Repeat("€", 25),
	} {
		rec := hs.do("POST", "/auth/register", "", map[string]any{"email": "long@b.co", "password": pw})
		if rec.Code != 400 {
			t.Fatalf("%s: status %d, want 400", name, rec.Code)
		}
		expectBody(t, rec, `{"error":"invalid_request"}`)
	}
}

func TestRegisterRejectsDevAccount(t *testing.T) {
	hs := newHarness(t)
	rec := hs.do("POST", "/auth/register", "", map[string]any{"email": "dev@local", "password": "password123"})
	expectStatus(t, rec, 409)
	expectBody(t, rec, `{"error":"email_already_exists"}`)
}

func TestAuthBodyErrors(t *testing.T) {
	hs := newHarness(t)
	for _, path := range []string{"/auth/register", "/auth/login", "/auth/refresh", "/auth/logout"} {
		expectStatus(t, hs.doRaw("POST", path, "", strings.NewReader(`{"email":`), nil), 400)
		rec := hs.doRaw("POST", path, "", bytes.NewReader(append([]byte(`{"x":"`), bytes.Repeat([]byte("a"), maxBodyBytes+10)...)), nil)
		expectStatus(t, rec, 413)
	}
}

func TestLoginWithPasswordHashedByNode(t *testing.T) {
	hs := newHarness(t)
	const nodeHash = "$2b$10$Yd8di3UA82FrevHpKn6pjuRApg6xyMVbXa7AiNAdOnQ4/vFe00jqG" // "password123"
	if _, err := hs.st.CreateUser(context.Background(), "old@user.co", nodeHash, nil); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, hs.do("POST", "/auth/login", "", map[string]any{"email": "old@user.co", "password": "password123"}), 200)
}

// SEC-3: an unknown email must cost about as much as a wrong password, so
// response time does not reveal which emails are registered. bcrypt at cost 10
// takes tens of milliseconds; without the dummy comparison the unknown-email
// path is a single indexed query (about a millisecond).
func TestLoginUnknownEmailPaysForBcrypt(t *testing.T) {
	hs := newHarness(t)
	hs.register("known@b.co")
	body := map[string]any{"email": "nobody@b.co", "password": "password123"}
	hs.do("POST", "/auth/login", "", body) // warm up
	var fastest time.Duration
	for range 3 {
		start := time.Now()
		expectStatus(t, hs.do("POST", "/auth/login", "", body), 401)
		if d := time.Since(start); fastest == 0 || d < fastest {
			fastest = d
		}
	}
	if fastest < 15*time.Millisecond {
		t.Fatalf("unknown-email login took %v; the dummy bcrypt comparison is missing", fastest)
	}
}

// A malformed stored hash is a failed login, never a 500.
func TestLoginWithMalformedStoredHash(t *testing.T) {
	hs := newHarness(t)
	if _, err := hs.st.CreateUser(context.Background(), "bad@b.co", "not-a-bcrypt-hash", nil); err != nil {
		t.Fatal(err)
	}
	rec := hs.do("POST", "/auth/login", "", map[string]any{"email": "bad@b.co", "password": "password123"})
	expectStatus(t, rec, 401)
	expectBody(t, rec, `{"error":"invalid_credentials"}`)
}

// Intended behaviour: every refresh token works exactly once. Using it returns
// a new access token and a new refresh token, and the old one is dead.
func TestRefreshRotates(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("r@b.co")

	rec := hs.refresh(reg.RefreshToken)
	expectStatus(t, rec, 200)
	next := decode[authResp](t, rec)
	if next.Token == "" || next.RefreshToken == "" || next.RefreshToken == reg.RefreshToken {
		t.Fatalf("refresh response %+v", next)
	}
	if sub, _ := hs.signer.Verify(next.Token); sub != reg.User.ID {
		t.Fatal("refreshed token has the wrong user")
	}
	// The new token works, and so does the one after it.
	third := decode[authResp](t, hsOK(t, hs.refresh(next.RefreshToken)))
	hsOK(t, hs.refresh(third.RefreshToken))
	expectStatus(t, hs.do("POST", "/auth/refresh", "", map[string]any{}), 400)
}

// Intended behaviour: presenting a consumed token means two parties hold a
// copy (theft, or two tabs racing), so the response is 401 with the same fixed
// body as an unknown token, and ALL of that user's refresh tokens are revoked.
// Other users are untouched. The user logs in again.
func TestRefreshReuseRevokesEveryTokenOfThatUser(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("reuse@b.co")
	other := hs.register("other@b.co")
	second := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": "reuse@b.co", "password": "password123"}))
	next := decode[authResp](t, hsOK(t, hs.refresh(reg.RefreshToken)))

	rec := hs.refresh(reg.RefreshToken) // replay of the consumed token
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)

	for name, tok := range map[string]string{"rotated successor": next.RefreshToken, "other session": second.RefreshToken} {
		rec := hs.refresh(tok)
		if rec.Code != 401 {
			t.Fatalf("%s survived the reuse revocation: %d", name, rec.Code)
		}
		expectBody(t, rec, invalidRefreshBody)
	}
	hsOK(t, hs.refresh(other.RefreshToken)) // a different user is unaffected
}

func TestRefreshUnknownAndRevokedTokensLookIdentical(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("same@b.co")
	hsOK(t, hs.refresh(reg.RefreshToken))
	reused := hs.refresh(reg.RefreshToken)
	unknown := hs.refresh("no-such-token")
	for _, rec := range []*httptest.ResponseRecorder{reused, unknown} {
		expectStatus(t, rec, 401)
		expectBody(t, rec, invalidRefreshBody)
	}
}

// The refresh token must never reach the logs, including on reuse.
func TestRefreshReuseLogsWithoutTheToken(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	lw := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) })
	hs := newHarnessWith(t, func(d *Deps) { d.Log = slog.New(slog.NewTextHandler(lw, nil)) })
	reg := hs.register("log@b.co")
	hsOK(t, hs.refresh(reg.RefreshToken))
	expectStatus(t, hs.refresh(reg.RefreshToken), 401)
	expectStatus(t, hs.refresh("another-unknown-token-value"), 401)

	mu.Lock()
	defer mu.Unlock()
	out := buf.String()
	if !strings.Contains(out, "reused") {
		t.Fatalf("reuse was not logged:\n%s", out)
	}
	for _, secret := range []string{reg.RefreshToken, "another-unknown-token-value"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output contains a refresh token:\n%s", out)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// Expiry is decided by the store's expiresAt. An expired token is rejected
// with the same 401 and does NOT revoke the user's other sessions (an expired
// token is not evidence of theft).
func TestRefreshExpiredToken(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("exp@b.co")
	live := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": "exp@b.co", "password": "password123"}))

	if _, err := pool.Exec(context.Background(),
		`UPDATE "RefreshToken" SET "expiresAt" = now() - interval '1 second' WHERE "tokenHash" = $1`,
		auth.HashRefreshToken(reg.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	rec := hs.refresh(reg.RefreshToken)
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)
	hsOK(t, hs.refresh(live.RefreshToken))
}

// The stored expiry is the documented refresh lifetime (auth.RefreshTTL).
func TestRefreshTokenLifetimeMatchesStore(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("ttl@b.co")
	var secs float64
	if err := pool.QueryRow(context.Background(),
		`SELECT extract(epoch from ("expiresAt" - now())) FROM "RefreshToken" WHERE "tokenHash" = $1`,
		auth.HashRefreshToken(reg.RefreshToken)).Scan(&secs); err != nil {
		t.Fatal(err)
	}
	if want := auth.RefreshTTL.Seconds(); secs < want-60 || secs > want+60 {
		t.Fatalf("stored token lives %.0fs, want about %.0fs", secs, want)
	}
}

// Only the SHA-256 hash is stored, never the token itself.
func TestRefreshTokenStoredOnlyAsHash(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("hash@b.co")
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM "RefreshToken" WHERE "tokenHash" = $1`, reg.RefreshToken).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plain token found in the table (n=%d, err=%v)", n, err)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM "RefreshToken" WHERE "tokenHash" = $1`, auth.HashRefreshToken(reg.RefreshToken)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hashed token not stored once (n=%d, err=%v)", n, err)
	}
}

// Intended behaviour: logout revokes that one refresh token. Using it later is
// a replay of a revoked token, so it is a 401 (and, like any replay, revokes
// the user's other sessions). Logout is idempotent and never leaks whether the
// token existed.
func TestLogoutRevokesRefreshToken(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("l@b.co")
	rec := hs.do("POST", "/auth/logout", "", map[string]any{"refreshToken": reg.RefreshToken})
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"ok":true}`)
	rec = hs.refresh(reg.RefreshToken)
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)

	for _, body := range []map[string]any{{"refreshToken": reg.RefreshToken}, {"refreshToken": "unknown"}, {}} {
		rec = hs.do("POST", "/auth/logout", "", body)
		expectStatus(t, rec, 200)
		expectBody(t, rec, `{"ok":true}`)
	}
}

func TestLogoutLeavesOtherSessionsAlone(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("two@b.co")
	second := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": "two@b.co", "password": "password123"}))
	expectStatus(t, hs.do("POST", "/auth/logout", "", map[string]any{"refreshToken": reg.RefreshToken}), 200)
	hsOK(t, hs.refresh(second.RefreshToken))
}

func TestDevLoginOnlyInDevelopment(t *testing.T) {
	hs := newHarness(t) // DevLogin: true
	type devResp struct {
		Token        string `json:"token"`
		RefreshToken string `json:"refreshToken"`
		UserID       string `json:"userId"`
	}
	a := decode[devResp](t, hs.do("POST", "/dev/login", "", nil))
	b := decode[devResp](t, hs.do("POST", "/dev/login", "", nil))
	if a.UserID == "" || a.UserID != b.UserID || a.RefreshToken == "" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if sub, err := hs.signer.Verify(a.Token); err != nil || sub != a.UserID {
		t.Fatalf("dev token sub %q err %v", sub, err)
	}

	prod := newHarnessWith(t, func(d *Deps) { d.DevLogin = false })
	rec := prod.do("POST", "/dev/login", "", nil)
	if rec.Code != 404 && rec.Code != 405 {
		t.Fatalf("dev login reachable in production: %d", rec.Code)
	}
}

func TestAuthRoutesAreRateLimited(t *testing.T) {
	hs := newHarnessWith(t, func(d *Deps) { d.AuthBurst = 2 })
	for _, path := range []string{"/auth/login", "/auth/register", "/auth/refresh"} {
		addr := "198.51.100." + map[string]string{"/auth/login": "1", "/auth/register": "2", "/auth/refresh": "3"}[path] + ":4000"
		codes := []int{}
		for range 3 {
			codes = append(codes, hs.doFrom(addr, "POST", path, "", nil, nil).Code)
		}
		if codes[0] == 429 || codes[1] == 429 || codes[2] != 429 {
			t.Fatalf("%s codes = %v", path, codes)
		}
	}
	// Another client is not affected.
	if rec := hs.doFrom("203.0.113.9:1", "POST", "/auth/login", "", nil, nil); rec.Code == 429 {
		t.Fatal("limiter is not per client")
	}
}
