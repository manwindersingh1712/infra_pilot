package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func box(t *testing.T) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := box(t)
	enc, err := b.Encrypt("s3cret value")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "enc:v1:") || strings.Contains(enc, "s3cret") || !IsEncrypted(enc) {
		t.Fatalf("enc = %q", enc)
	}
	if got, err := b.Decrypt(enc); err != nil || got != "s3cret value" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	b := box(t)
	x, _ := b.Encrypt("same")
	y, _ := b.Encrypt("same")
	if x == y {
		t.Fatal("two encryptions of the same value must differ")
	}
}

func TestPlainTextPassesThrough(t *testing.T) {
	if got, err := box(t).Decrypt("legacy-plain"); err != nil || got != "legacy-plain" {
		t.Fatalf("got %q err %v", got, err)
	}
	if IsEncrypted("legacy-plain") {
		t.Fatal("plain text reported as encrypted")
	}
}

func TestTamperedOrWrongKeyFails(t *testing.T) {
	b := box(t)
	enc, _ := b.Encrypt("v")
	tampered := enc[:len(enc)-2] + "AA"
	if _, err := b.Decrypt(tampered); err == nil {
		t.Fatal("tampered value must not decrypt")
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("wrong key must not decrypt")
	}
	if _, err := b.Decrypt("enc:v1:%%%"); err == nil {
		t.Fatal("bad base64 must fail")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected (AES-256 only)")
	}
}

func TestRandomPassword(t *testing.T) {
	a, err := RandomPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RandomPassword()
	if len(a) != 32 || a == b || strings.ContainsAny(a, "+/=\"'$ ") {
		t.Fatalf("a=%q b=%q", a, b)
	}
}
