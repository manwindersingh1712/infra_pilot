// Package gitsrc validates and redacts build sources (repo URL, branch,
// commit) that tenants control. The API and the worker share these rules.
package gitsrc

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// ErrInvalid wraps every validation failure. Messages name the field, never the value.
var ErrInvalid = errors.New("invalid build source")

var (
	branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	refRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{3,99}$`)
)

func invalid(field, why string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, why)
}

// ValidRepoURL accepts only https URLs with a host.
func ValidRepoURL(s string) error {
	if len(s) > 2048 {
		return invalid("repo url", "is too long")
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return invalid("repo url", "contains characters outside printable ASCII")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return invalid("repo url", "is not parseable")
	}
	if u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return invalid("repo url", "must be an https URL with a host")
	}
	if strings.HasPrefix(u.Hostname(), "-") {
		return invalid("repo url", "has an invalid host")
	}
	return nil
}

// ValidBranch accepts ref-safe branch names that cannot be read as options.
func ValidBranch(s string) error {
	if !branchRe.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") ||
		strings.Contains(s, "@{") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") ||
		strings.HasSuffix(s, ".lock") {
		return invalid("branch", "is not a safe ref name")
	}
	return nil
}

// ValidRef validates a commit reference, also used as the image tag.
func ValidRef(s string) error {
	if !refRe.MatchString(s) || strings.Contains(s, "..") {
		return invalid("commit", "is not a safe ref")
	}
	return nil
}

var imageTagRe = regexp.MustCompile(`^[a-z0-9][A-Za-z0-9._:/-]*$`)

// ValidImageTag checks the image reference the build pushes: at most 255 bytes,
// the docker reference shape (so it cannot be read as an option), and ending
// in ":<ref>". Messages never echo the value.
func ValidImageTag(tag, ref string) error {
	if len(tag) > 255 {
		return invalid("image tag", "is too long")
	}
	if !imageTagRe.MatchString(tag) {
		return invalid("image tag", "is not a valid image reference")
	}
	if ref == "" || !strings.HasSuffix(tag, ":"+ref) {
		return invalid("image tag", "does not end in the commit")
	}
	return nil
}

// Validate runs all three checks.
func Validate(repoURL, branch, ref string) error {
	if err := ValidRepoURL(repoURL); err != nil {
		return err
	}
	if err := ValidBranch(branch); err != nil {
		return err
	}
	return ValidRef(ref)
}

// RedactURL masks userinfo (including a token used as the username) and
// query values, and drops the fragment.
func RedactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "[unparseable]"
	}
	u.Fragment, u.RawFragment = "", ""
	if u.RawQuery != "" {
		parts := strings.Split(u.RawQuery, "&")
		for i, p := range parts {
			if k, _, ok := strings.Cut(p, "="); ok {
				parts[i] = k + "=****"
			} else {
				parts[i] = "****"
			}
		}
		u.RawQuery = strings.Join(parts, "&")
	}
	if u.User != nil {
		// url.User("****") would be percent-encoded, so mask via a placeholder.
		u.User = url.User("REDACTED")
		return strings.Replace(u.String(), "REDACTED@", "****@", 1)
	}
	return u.String()
}

// minQuerySecret is the shortest query value Scrub masks; shorter ones would
// mangle unrelated text.
const minQuerySecret = 3

// Scrub removes the URL's credentials and query values from msg (for example
// git stderr): the full URL text first, then each secret on its own, in
// escaped and unescaped forms.
func Scrub(msg, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.User == nil && u.RawQuery == "") {
		return msg
	}
	secrets := []string{rawURL}
	if u.User != nil {
		secrets = append(secrets, u.User.String(), u.User.Username())
		if pw, ok := u.User.Password(); ok {
			secrets = append(secrets, url.PathEscape(pw), url.QueryEscape(pw), pw)
		}
		// The raw userinfo text, as the caller wrote it.
		if rest, ok := strings.CutPrefix(rawURL, "https://"); ok {
			auth, _, _ := strings.Cut(rest, "/")
			auth, _, _ = strings.Cut(auth, "?")
			if i := strings.LastIndex(auth, "@"); i >= 0 {
				user, pw, hasPw := strings.Cut(auth[:i], ":")
				secrets = append(secrets, auth[:i], user)
				if hasPw {
					secrets = append(secrets, pw)
				}
			}
		}
	}
	for _, p := range strings.Split(u.RawQuery, "&") {
		_, v, ok := strings.Cut(p, "=")
		if !ok {
			v = p
		}
		if dec, err := url.QueryUnescape(v); err == nil && len(dec) >= minQuerySecret {
			secrets = append(secrets, dec)
		}
		if len(v) >= minQuerySecret {
			secrets = append(secrets, v)
		}
	}
	// Longest first, so a secret that contains another is masked whole.
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, sec := range secrets {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "****")
		}
	}
	return msg
}
