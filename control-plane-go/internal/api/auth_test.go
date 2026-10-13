package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

// PROD-4, SEC-5: password limits are in bytes (bcrypt reads only 72) and have
// their own codes on register. A bad email is reported first as invalid_request.
func TestRegisterPasswordLengthLimits(t *testing.T) {
	hs := newHarness(t)
	for _, tc := range []struct {
		name, pw, wantErr string
	}{
		{"7 ascii bytes", strings.Repeat("a", 7), "password_too_short"},
		{"empty", "", "password_too_short"},
		{"7 bytes in 3 runes", "€€a", "password_too_short"}, // 3+3+1 bytes = 7
		{"73 ascii bytes", strings.Repeat("a", 73), "password_too_long"},
		{"75 bytes in 25 runes", strings.Repeat("€", 25), "password_too_long"},
		{"8 ascii bytes", strings.Repeat("a", 8), ""},
		{"8 bytes in 3 runes", "€€aa", ""},
		{"72 ascii bytes", strings.Repeat("a", 72), ""},
		{"72 bytes in 24 runes", strings.Repeat("€", 24), ""},
	} {
		email := strings.ReplaceAll(tc.name, " ", "") + "@b.co"
		rec := hs.do("POST", "/auth/register", "", map[string]any{"email": email, "password": tc.pw})
		if tc.wantErr == "" {
			if rec.Code != 200 {
				t.Fatalf("%s: status %d, want 200: %s", tc.name, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != 400 {
			t.Fatalf("%s: status %d, want 400", tc.name, rec.Code)
		}
		expectBody(t, rec, `{"error":"`+tc.wantErr+`"}`)
	}
	// The email is checked before the password.
	rec := hs.do("POST", "/auth/register", "", map[string]any{"email": "nope", "password": "x"})
	expectStatus(t, rec, 400)
	expectBody(t, rec, `{"error":"invalid_request"}`)
}

// Login has no password length check (existing hashes, Node parity): an
// over-long or short password is just wrong credentials.
func TestLoginHasNoPasswordLengthCheck(t *testing.T) {
	hs := newHarness(t)
	hs.register("len@b.co")
	for _, pw := range []string{"x", strings.Repeat("a", 73)} {
		rec := hs.do("POST", "/auth/login", "", map[string]any{"email": "len@b.co", "password": pw})
		expectStatus(t, rec, 401)
		expectBody(t, rec, `{"error":"invalid_credentials"}`)
	}
}

// emailOfLen returns a syntactically valid address of exactly n bytes.
func emailOfLen(n int) string {
	const domain = "@b.co"
	return strings.Repeat("a", n-len(domain)) + domain
}

// Email is capped at 254 bytes (RFC 5321) before parsing, on register and login.
func TestEmailLengthCap(t *testing.T) {
	hs := newHarness(t)
	long, ok := emailOfLen(255), emailOfLen(254)

	rec := hs.do("POST", "/auth/register", "", map[string]any{"email": long, "password": "password123"})
	expectStatus(t, rec, 400)
	expectBody(t, rec, `{"error":"invalid_request"}`)
	expectStatus(t, hs.do("POST", "/auth/register", "", map[string]any{"email": ok, "password": "password123"}), 200)

	rec = hs.do("POST", "/auth/login", "", map[string]any{"email": long, "password": "password123"})
	expectStatus(t, rec, 400)
	expectBody(t, rec, `{"error":"invalid_request"}`)
	expectStatus(t, hs.do("POST", "/auth/login", "", map[string]any{"email": ok, "password": "password123"}), 200)

	// Multi-byte local part: 127 two-byte runes = 254 bytes of local part alone, over the cap.
	rec = hs.do("POST", "/auth/register", "", map[string]any{"email": strings.Repeat("é", 127) + "@b.co", "password": "password123"})
	expectStatus(t, rec, 400)
}

// Name is capped at 100 characters (runes, not bytes); null or absent is fine.
func TestRegisterNameLengthCap(t *testing.T) {
	hs := newHarness(t)
	for i, tc := range []struct {
		name   any
		wantOK bool
	}{
		{strings.Repeat("a", 100), true},
		{strings.Repeat("€", 100), true}, // 300 bytes, 100 characters
		{strings.Repeat("a", 101), false},
		{strings.Repeat("€", 101), false},
		{nil, true},
	} {
		body := map[string]any{"email": fmt.Sprintf("n%d@b.co", i), "password": "password123"}
		if tc.name != nil {
			body["name"] = tc.name
		}
		rec := hs.do("POST", "/auth/register", "", body)
		if tc.wantOK {
			expectStatus(t, rec, 200)
			continue
		}
		expectStatus(t, rec, 400)
		expectBody(t, rec, `{"error":"invalid_request"}`)
	}
	expectStatus(t, hs.do("POST", "/auth/register", "", map[string]any{"email": "absent@b.co", "password": "password123"}), 200)
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

// backdateRotation makes a rotated token look like it was rotated secs ago.
func backdateRotation(t *testing.T, plain string, secs int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE "RefreshToken" SET "rotatedAt" = now() - $2::int * interval '1 second' WHERE "tokenHash" = $1`,
		auth.HashRefreshToken(plain), secs); err != nil {
		t.Fatal(err)
	}
}

// Intended behaviour: presenting a rotated token after the grace window means
// two parties hold a copy (theft), so the response is 401 with the same fixed
// body as an unknown token, and that token's FAMILY (one login) is revoked.
// The user's other sessions and other users are untouched.
func TestRefreshReuseRevokesTheFamilyOnly(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("reuse@b.co")
	other := hs.register("other@b.co")
	second := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": "reuse@b.co", "password": "password123"}))
	next := decode[authResp](t, hsOK(t, hs.refresh(reg.RefreshToken)))
	backdateRotation(t, reg.RefreshToken, 11)

	rec := hs.refresh(reg.RefreshToken) // replay of the rotated token, outside the grace window
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)

	rec = hs.refresh(next.RefreshToken)
	if rec.Code != 401 {
		t.Fatalf("rotated successor survived the reuse revocation: %d", rec.Code)
	}
	expectBody(t, rec, invalidRefreshBody)
	hsOK(t, hs.refresh(second.RefreshToken)) // the user's other session survives
	hsOK(t, hs.refresh(other.RefreshToken))  // a different user is unaffected
}

// A replay inside the grace window is a benign race (two tabs): plain 401, and
// the winner's new token keeps working.
func TestRefreshReplayInsideGraceRevokesNothing(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("grace@b.co")
	next := decode[authResp](t, hsOK(t, hs.refresh(reg.RefreshToken)))
	rec := hs.refresh(reg.RefreshToken)
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)
	hsOK(t, hs.refresh(next.RefreshToken))
}

// (f) Two concurrent refreshes with one token: one 200, one 401. The winner's
// token works and the user's other session is untouched.
func TestRefreshConcurrentRequests(t *testing.T) {
	hs := newHarness(t)
	for rep := range 10 {
		reg := hs.register(fmt.Sprintf("conc%d@b.co", rep))
		second := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": fmt.Sprintf("conc%d@b.co", rep), "password": "password123"}))
		start := make(chan struct{})
		recs := make([]*httptest.ResponseRecorder, 2)
		var wg sync.WaitGroup
		for i := range recs {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; recs[i] = hs.refresh(reg.RefreshToken) }()
		}
		close(start)
		wg.Wait()
		var winner *httptest.ResponseRecorder
		for _, rec := range recs {
			switch rec.Code {
			case 200:
				if winner != nil {
					t.Fatalf("rep %d: two refreshes succeeded", rep)
				}
				winner = rec
			case 401:
				expectBody(t, rec, invalidRefreshBody)
			default:
				t.Fatalf("rep %d: status %d: %s", rep, rec.Code, rec.Body.String())
			}
		}
		if winner == nil {
			t.Fatalf("rep %d: no refresh succeeded", rep)
		}
		hsOK(t, hs.refresh(decode[authResp](t, winner).RefreshToken))
		hsOK(t, hs.refresh(second.RefreshToken))
	}
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
	expectStatus(t, hs.refresh(reg.RefreshToken), 401) // inside the grace window
	mu.Lock()
	graceOut := buf.String()
	mu.Unlock()
	if !strings.Contains(graceOut, "replayed inside grace window") {
		t.Fatalf("grace replay was not logged:\n%s", graceOut)
	}
	backdateRotation(t, reg.RefreshToken, 11)
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

// Intended behaviour: logout revokes the presenter's family. Using the token
// later is a plain 401 with no further effect. Logout is idempotent and never
// leaks whether the token existed.
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

// (e) Logging out with a stale (rotated) token of a live chain kills that chain.
func TestLogoutWithStaleTokenKillsTheChain(t *testing.T) {
	hs := newHarness(t)
	reg := hs.register("stale@b.co")
	second := decode[authResp](t, hs.do("POST", "/auth/login", "", map[string]any{"email": "stale@b.co", "password": "password123"}))
	next := decode[authResp](t, hsOK(t, hs.refresh(reg.RefreshToken)))
	expectBody(t, hs.do("POST", "/auth/logout", "", map[string]any{"refreshToken": reg.RefreshToken}), `{"ok":true}`)
	rec := hs.refresh(next.RefreshToken)
	expectStatus(t, rec, 401)
	expectBody(t, rec, invalidRefreshBody)
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

// DNS rebinding: a hostile page that rebinds its name to 127.0.0.1 reaches the
// dev API with its own Host. Only /dev/login, which needs no credentials, is
// gated on the Host.
func TestDevLoginRejectsNonLoopbackHost(t *testing.T) {
	hs := newHarness(t)
	for _, host := range []string{
		"localhost", "localhost:8080", "LOCALHOST:5173", "127.0.0.1", "127.0.0.1:8080", "[::1]", "[::1]:8080",
	} {
		if rec := hs.doRaw("POST", "/dev/login", "", nil, map[string]string{"Host": host}); rec.Code != 200 {
			t.Errorf("Host %q: %d %s", host, rec.Code, rec.Body.String())
		}
	}
	for _, host := range []string{
		"evil.example", "evil.example:8080", "localhost.evil.example", "evil-localhost", "127.0.0.2:8080",
		"127.0.0.1.evil.example", "0.0.0.0:8080", "[::2]:8080", "192.168.1.5:8080", "localhost@evil.example", "",
	} {
		rec := hs.doRaw("POST", "/dev/login", "", nil, map[string]string{"Host": host})
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: %d, want 403", host, rec.Code)
			continue
		}
		expectBody(t, rec, `{"error":"forbidden_host"}`)
		if strings.Contains(rec.Body.String(), "token") {
			t.Errorf("Host %q: 403 body carries a token", host)
		}
	}
	// Other routes are not Host-gated (nginx and the UI proxy use other names).
	if rec := hs.doRaw("POST", "/auth/login", "", nil, map[string]string{"Host": "api.example.test"}); rec.Code == http.StatusForbidden {
		t.Fatal("/auth/login must not check the Host")
	}
	if rec := hs.doRaw("GET", "/healthz", "", nil, map[string]string{"Host": "api.example.test"}); rec.Code != 200 {
		t.Fatalf("/healthz with a foreign Host: %d", rec.Code)
	}
}

func TestAuthRoutesAreRateLimited(t *testing.T) {
	hs := newHarnessWith(t, func(d *Deps) { d.AuthBurst = 2; d.RefreshBurst = 2 })
	for i, path := range []string{"/auth/login", "/auth/register", "/auth/refresh", "/auth/logout"} {
		addr := fmt.Sprintf("198.51.100.%d:4000", i+1)
		codes := []int{}
		for range 3 {
			codes = append(codes, hs.doFrom(addr, "POST", path, "", nil, nil).Code)
		}
		// A fresh address per path keeps the four buckets from interfering.
		if codes[0] == 429 || codes[1] == 429 || codes[2] != 429 {
			t.Fatalf("%s codes = %v", path, codes)
		}
	}
	// Another client is not affected.
	if rec := hs.doFrom("203.0.113.9:1", "POST", "/auth/login", "", nil, nil); rec.Code == 429 {
		t.Fatal("limiter is not per client")
	}
}

// exhaust sends requests until one is rate limited and reports how many passed.
func exhaust(hs *harness, addr, path string) int {
	for n := range 100 {
		if hs.doFrom(addr, "POST", path, "", nil, nil).Code == 429 {
			return n
		}
	}
	return 100
}

// SEC-2: register/login and refresh/logout are separate buckets per IP.
func TestRateLimitBucketsAreIndependent(t *testing.T) {
	const addr = "198.51.100.50:4000"
	t.Run("exhausting login does not block refresh or logout", func(t *testing.T) {
		hs := newHarnessWith(t, func(d *Deps) { d.AuthBurst = 2; d.RefreshBurst = 3 })
		if n := exhaust(hs, addr, "/auth/login"); n != 2 {
			t.Fatalf("login burst = %d, want 2", n)
		}
		if n := exhaust(hs, addr, "/auth/register"); n != 0 {
			t.Fatalf("register shares the login bucket; passed %d", n)
		}
		for range 3 {
			if rec := hs.doFrom(addr, "POST", "/auth/refresh", "", nil, nil); rec.Code == 429 {
				t.Fatal("refresh blocked by an exhausted login bucket")
			}
		}
	})
	t.Run("exhausting refresh does not block login or register", func(t *testing.T) {
		hs := newHarnessWith(t, func(d *Deps) { d.AuthBurst = 2; d.RefreshBurst = 3 })
		if n := exhaust(hs, addr, "/auth/refresh"); n != 3 {
			t.Fatalf("refresh burst = %d, want 3", n)
		}
		if n := exhaust(hs, addr, "/auth/logout"); n != 0 {
			t.Fatalf("logout does not share the refresh bucket; passed %d", n)
		}
		for _, path := range []string{"/auth/login", "/auth/register"} {
			if rec := hs.doFrom(addr, "POST", path, "", nil, nil); rec.Code == 429 {
				t.Fatalf("%s blocked by an exhausted refresh bucket", path)
			}
		}
	})
}

// Zero values mean the documented defaults: 5 for register/login, 20 for refresh/logout.
func TestRateLimitDefaultBursts(t *testing.T) {
	hs := newHarnessWith(t, func(d *Deps) { d.AuthBurst = 0; d.RefreshBurst = 0 })
	if n := exhaust(hs, "198.51.100.60:1", "/auth/login"); n != 5 {
		t.Fatalf("default strict burst = %d, want 5", n)
	}
	if n := exhaust(hs, "198.51.100.61:1", "/auth/refresh"); n != 20 {
		t.Fatalf("default refresh burst = %d, want 20", n)
	}
}

// The refill interval of each limiter is what turns a burst into a rate:
// register/login 10 per minute, refresh/logout 60 per minute (STAFF-1).
func TestServerLimiterRefillIntervals(t *testing.T) {
	s := newServer(Deps{})
	if s.authLimit.every != 6*time.Second {
		t.Fatalf("register/login refills every %v, want 6s (10/min)", s.authLimit.every)
	}
	if s.refreshLimit.every != time.Second {
		t.Fatalf("refresh/logout refills every %v, want 1s (60/min)", s.refreshLimit.every)
	}
	if s.authLimit.burst != 5 || s.refreshLimit.burst != 20 {
		t.Fatalf("default bursts %d/%d, want 5/20", s.authLimit.burst, s.refreshLimit.burst)
	}
}
