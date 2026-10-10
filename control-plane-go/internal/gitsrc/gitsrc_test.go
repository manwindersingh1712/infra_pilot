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
	if err := ValidRepoURL("https://xn--bcher-kva.example/x"); err != nil {
		t.Errorf("punycode host: %v", err)
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
		"https://-oProxyCommand=x/y", "https://h/x y", "https://h/x\ny",
		"https://h/x\u2028y", "https://h/x\u0085y", "https://h/x\u00a0y", "https://h/x\u202ey", "https://h/x\u200by",
		"https://\uff48/x", "https://h/\uff58", "https://h/caf\u00e9", "https://h/x\ty", "https://h/" + strings.Repeat("a", 2100), ""} {
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

func TestRedactURLQueryAndFragment(t *testing.T) {
	for in, want := range map[string]string{
		"https://h/x?token=ghp_Q":          "https://h/x?token=****",
		"https://h/x?a=1&token=ghp_Q":      "https://h/x?a=****&token=****",
		"https://h/x?ghp_Q":                "https://h/x?****",
		"https://h/x#frag-secret":          "https://h/x",
		"https://u:pw@h/x?token=ghp_Q#sec": "https://****@h/x?token=****",
		"https://h/x":                      "https://h/x",
	} {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScrubFullURLAndQuery(t *testing.T) {
	raw := "https://h/x?token=ghp_Q&b=v%2F1"
	for _, msg := range []string{
		"fatal: unable to access '" + raw + "': 403",
		"bad token ghp_Q",
		"bad value v/1",
		"bad value v%2F1",
	} {
		out := Scrub(msg, raw)
		for _, leak := range []string{"ghp_Q", "v/1", "v%2F1", "token="} {
			if strings.Contains(out, leak) {
				t.Errorf("Scrub(%q) = %q leaks %q", msg, out, leak)
			}
		}
	}
	if out := Scrub("plain", raw); out != "plain" {
		t.Errorf("got %q", out)
	}
}

func TestValidImageTag(t *testing.T) {
	for _, c := range [][2]string{
		{"localhost:5001/svc1:abc1234", "abc1234"},
		{"registry.example.com/team/svc:main", "main"},
		{"svc:HEAD", "HEAD"},
	} {
		if err := ValidImageTag(c[0], c[1]); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	for _, c := range [][2]string{
		{"-oops/svc:abc1234", "abc1234"},
		{"localhost:5001/svc:other", "abc1234"},
		{"localhost:5001/svc", "abc1234"},
		{"svc:abc1234", ""},
		{"Svc:abc1234", "abc1234"},
		{"s v:abc1234", "abc1234"},
		{"r/\u00e9:abc1234", "abc1234"},
		{"r/x\u2028:abc1234", "abc1234"},
		{strings.Repeat("a", 250) + ":abc1234", "abc1234"},
		{"", "abc1234"},
	} {
		err := ValidImageTag(c[0], c[1])
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: err = %v", c, err)
		} else if c[0] != "" && strings.Contains(err.Error(), c[0]) {
			t.Errorf("%q: message leaks tag: %v", c, err)
		}
	}
}

func TestScrubEscapedAndSpecialSecrets(t *testing.T) {
	cases := []struct{ raw, msg string }{
		// password-only userinfo, escaped
		{"https://:p%40ss@h/x", "fatal: 'https://:p%40ss@h/x/' denied"},
		{"https://:p%40ss@h/x", "password p%40ss and p@ss"},
		// special characters in a token username
		{"https://gh%2Fp_T%3AK@h/x", "token gh%2Fp_T%3AK and gh/p_T:K"},
		{"https://us%20er:p%2Fw%3Fd@h/x", "echo p%2Fw%3Fd p/w?d us%20er"},
		// query token
		{"https://h/x?token=ghp_Q", "bad ghp_Q in https://h/x?token=ghp_Q"},
	}
	for _, c := range cases {
		out := Scrub(c.msg, c.raw)
		for _, leak := range []string{"p%40ss", "p@ss", "gh%2Fp_T%3AK", "gh/p_T:K", "p%2Fw%3Fd", "p/w?d", "us%20er", "ghp_Q"} {
			if strings.Contains(out, leak) {
				t.Errorf("Scrub(%q, %q) = %q leaks %q", c.msg, c.raw, out, leak)
			}
		}
	}
}

func TestRedactURLSpecialChars(t *testing.T) {
	for _, in := range []string{
		"https://:p%40ss@h/x",
		"https://gh%2Fp_T%3AK@h/x",
		"https://us%20er:p%2Fw%3Fd@h/x?token=ghp_Q#frag",
	} {
		out := RedactURL(in)
		for _, leak := range []string{"p%40ss", "p@ss", "gh%2Fp", "us%20er", "p%2Fw", "ghp_Q", "frag"} {
			if strings.Contains(out, leak) {
				t.Errorf("RedactURL(%q) = %q leaks %q", in, out, leak)
			}
		}
		if !strings.Contains(out, "****@h") {
			t.Errorf("RedactURL(%q) = %q, want ****@h", in, out)
		}
	}
}
