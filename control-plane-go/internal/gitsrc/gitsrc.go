// Package gitsrc validates and redacts build sources (repo URL, branch,
// commit) that tenants control. The API and the worker share these rules.
package gitsrc

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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
		if r <= 0x20 || r == 0x7f {
			return invalid("repo url", "contains control characters or whitespace")
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

// RedactURL masks userinfo, including a token used as the username.
func RedactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return "[unparseable]"
	}
	if u.User != nil {
		// url.User("****") would be percent-encoded, so mask via a placeholder.
		u.User = url.User("REDACTED")
		return strings.Replace(u.String(), "REDACTED@", "****@", 1)
	}
	return u.String()
}

// Scrub removes the URL's credentials from msg (for example git stderr).
func Scrub(msg, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return msg
	}
	secrets := []string{u.User.String()}
	if pw, ok := u.User.Password(); ok {
		// The escaped form is also masked in case git echoes it on its own.
		secrets = append(secrets, url.PathEscape(pw), pw)
	}
	secrets = append(secrets, u.User.Username())
	for _, s := range secrets {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "****")
		}
	}
	return msg
}
