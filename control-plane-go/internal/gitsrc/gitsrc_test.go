package gitsrc

import (
	"errors"
	"strings"
	"testing"
)

func TestAccepted(t *testing.T) {
	for _, u := range []string{"https://github.com/x/y.git", "https://tok@github.com/x/y"} {
		if err := ValidRepoURL(u); err != nil {
			t.Errorf("url %q: %v", u, err)
		}
	}
	for _, b := range []string{"main", "feature/a-b"} {
		if err := ValidBranch(b); err != nil {
			t.Errorf("branch %q: %v", b, err)
		}
	}
	for _, r := range []string{"HEAD", "main", "abcd", strings.Repeat("a1", 20)} {
		if err := ValidRef(r); err != nil {
			t.Errorf("ref %q: %v", r, err)
		}
	}
	if err := Validate("https://github.com/x/y.git", "main", "abcd"); err != nil {
		t.Error(err)
	}
}

func TestRejected(t *testing.T) {
	check := func(kind, in string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s %q accepted", kind, in)
			return
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s %q: not ErrInvalid: %v", kind, in, err)
		}
		if in != "" && strings.Contains(err.Error(), in) {
			t.Errorf("%s %q: message leaks input: %v", kind, in, err)
		}
	}
	for _, u := range []string{"file:///etc", "ext::sh -c id", "ssh://git@h/x", "git@github.com:x/y.git", "http://h/x",
		"https://-oProxyCommand=x/y", "https://h/x y", "https://h/x\ny", "https://h/" + strings.Repeat("a", 2100), ""} {
		check("url", u, ValidRepoURL(u))
	}
	for _, b := range []string{"--upload-pack=x", "-b", "a..b", "a/", "x.lock", "a//b", "a@{b", "a.", ""} {
		check("branch", b, ValidBranch(b))
	}
	for _, r := range []string{"--upload-pack=touch /tmp/pwn", "-x12", "ab", "a..bc", "feature/x", ""} {
		check("ref", r, ValidRef(r))
	}
	check("validate", "--x", Validate("https://h/x", "main", "--x"))
	check("validate", "-b", Validate("https://h/x", "-b", "abcd"))
	check("validate", "file:///etc", Validate("file:///etc", "main", "abcd"))
}

func TestRedactURL(t *testing.T) {
	for _, in := range []string{"https://user:pw1@h/x", "https://ghp_T0K@h/x"} {
		out := RedactURL(in)
		if strings.Contains(out, "pw1") || strings.Contains(out, "ghp_T0K") || strings.Contains(out, "user") || !strings.Contains(out, "****@h") {
			t.Errorf("RedactURL(%q) = %q", in, out)
		}
	}
	if got := RedactURL("https://h/x"); got != "https://h/x" {
		t.Errorf("no userinfo changed: %q", got)
	}
	if got := RedactURL("https://h/%zz"); got != "[unparseable]" {
		t.Errorf("got %q", got)
	}
}

func TestScrub(t *testing.T) {
	raw := "https://user:pw%21@h/x"
	for _, msg := range []string{
		"fatal: unable to access 'https://user:pw%21@h/x/': 403",
		"fatal: unable to access 'https://user:pw!@h/x/': 403",
		"auth failed for user with pw! and pw%21",
	} {
		out := Scrub(msg, raw)
		if strings.Contains(out, "pw!") || strings.Contains(out, "pw%21") || strings.Contains(out, "user:") {
			t.Errorf("Scrub(%q) = %q", msg, out)
		}
	}
	tok := "https://ghp_T0K@h/x"
	if out := Scrub("fatal: 'https://ghp_T0K@h/x/'", tok); strings.Contains(out, "ghp_") {
		t.Errorf("token not scrubbed: %q", out)
	}
	if out := Scrub("plain", "https://h/x"); out != "plain" {
		t.Errorf("got %q", out)
	}
}
