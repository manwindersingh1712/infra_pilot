package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	nodeToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1c2VyXzEyMyIsImlhdCI6MTc5MTMxMjA3MX0.gS3vLdXmKgPoS0gWqmoWfPfxPG2Oa0V9TIwUYrpgHI0"
	nodeHash  = "$2b$10$Yd8di3UA82FrevHpKn6pjuRApg6xyMVbXa7AiNAdOnQ4/vFe00jqG" // "password123"
)

func mustSigner(t *testing.T, secret string) *Signer {
	t.Helper()
	s, err := NewSigner(secret)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSignerRejectsEmptySecret(t *testing.T) {
	if s, err := NewSigner(""); err == nil || s != nil {
		t.Fatalf("s=%v err=%v", s, err)
	}
}

func TestSignVerifyRoundTripWithExpiry(t *testing.T) {
	s := mustSigner(t, "k")
	tok, err := s.Sign("u1")
	if err != nil {
		t.Fatal(err)
	}
	if sub, err := s.Verify(tok); err != nil || sub != "u1" {
		t.Fatalf("sub=%q err=%v", sub, err)
	}
	claims := jwt.MapClaims{}
	jwt.NewParser().ParseUnverified(tok, claims)
	exp, _ := claims.GetExpirationTime()
	if exp == nil || time.Until(exp.Time) > AccessTTL || time.Until(exp.Time) < AccessTTL-time.Minute {
		t.Fatalf("exp = %v", exp)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	s := mustSigner(t, "k")
	s.now = func() time.Time { return time.Now().Add(-time.Hour) }
	tok, _ := s.Sign("u1")
	if _, err := mustSigner(t, "k").Verify(tok); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestVerifyRejectsNodeTokensWithoutExpiry(t *testing.T) {
	// Node issued tokens without exp. After the Plan 2 cutover they must stop
	// working, so every user logs in once and gets an expiring token.
	_, err := mustSigner(t, "test-secret").Verify(nodeToken)
	if err == nil || !strings.Contains(err.Error(), "exp") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyRejectsBadTokens(t *testing.T) {
	good, _ := mustSigner(t, "a").Sign("u1")
	for _, tok := range []string{"", "garbage", good} {
		if _, err := mustSigner(t, "other").Verify(tok); err == nil {
			t.Errorf("Verify(%.20q) should fail", tok)
		}
	}
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	unsigned, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := mustSigner(t, "k").Verify(unsigned); err == nil {
		t.Fatal("alg=none must be rejected")
	}
}

func TestVerifyPinsHS256(t *testing.T) {
	// Right secret, valid exp and sub: only the algorithm is wrong.
	claims := jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()}
	for _, m := range []jwt.SigningMethod{jwt.SigningMethodHS384, jwt.SigningMethodHS512} {
		tok, err := jwt.NewWithClaims(m, claims).SignedString([]byte("k"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mustSigner(t, "k").Verify(tok); err == nil {
			t.Errorf("%s token accepted", m.Alg())
		}
	}
}

func TestVerifyRequiresSub(t *testing.T) {
	for name, claims := range map[string]jwt.MapClaims{
		"missing": {"exp": time.Now().Add(time.Hour).Unix()},
		"empty":   {"sub": "", "exp": time.Now().Add(time.Hour).Unix()},
	} {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("k"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := mustSigner(t, "k").Verify(tok); err == nil {
			t.Errorf("token with %s sub accepted", name)
		}
	}
}

func TestRefreshTokens(t *testing.T) {
	plain, hash, err := NewRefreshToken()
	if err != nil || len(plain) < 40 || hash != HashRefreshToken(plain) || len(hash) != 64 {
		t.Fatalf("plain=%q hash=%q err=%v", plain, hash, err)
	}
	other, _, _ := NewRefreshToken()
	if other == plain {
		t.Fatal("refresh tokens must be unique")
	}
}

func TestCheckPasswordAcceptsNodeHash(t *testing.T) {
	if !CheckPassword(nodeHash, "password123") || CheckPassword(nodeHash, "wrong") {
		t.Fatal("bcrypt compatibility broken")
	}
}

func TestHashPasswordUsesCost10(t *testing.T) {
	h, err := HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	if cost, _ := bcrypt.Cost([]byte(h)); cost != 10 || !CheckPassword(h, "password123") {
		t.Fatalf("cost=%d", cost)
	}
}
