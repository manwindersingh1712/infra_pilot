package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/config"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
)

var (
	// errSuspect: enc:v1: values that do not decrypt, and --allow-suspect was not given.
	errSuspect = errors.New("refusing to write: some enc:v1: values do not decrypt with this key (nothing was changed)")
	// errNeedYes: a real run without --yes. Exit code 2.
	errNeedYes = &exitCodeError{code: 2, msg: "not confirmed: nothing was changed; use --dry-run to preview or --yes to apply"}
	// errWrongKey: every enc:v1: value fails and plain rows exist. Not overridable.
	errWrongKey = errors.New("refusing to write: no enc:v1: value decrypts with this ENV_ENCRYPTION_KEY, so it is almost certainly the wrong key (nothing was changed)")
)

// exitCodeError carries a specific process exit code.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

// exitCode maps an error to the process exit code: 0 ok, 2 not confirmed, 1 otherwise.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := errors.AsType[*exitCodeError](err); ok {
		return ec.code
	}
	return 1
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9._\-]+$|^\[[0-9A-Fa-f:.]+\]$`)
var dbRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// targetOf renders "host:port/dbname" from a Postgres URL. It never returns the
// user, the password, query parameters or any part of an unparseable URL.
func targetOf(raw string) string {
	const unknown = "(unparseable DATABASE_URL, not shown)"
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return unknown
	}
	host, port := u.Hostname(), u.Port()
	if strings.Contains(u.Host, "]") {
		host = "[" + host + "]"
	}
	db := strings.TrimPrefix(u.Path, "/")
	if host == "" || !hostRe.MatchString(host) || !dbRe.MatchString(db) || (port != "" && !portRe.MatchString(port)) {
		return unknown
	}
	if port == "" {
		port = "5432"
	}
	return host + ":" + port + "/" + db
}

var portRe = regexp.MustCompile(`^[0-9]{1,5}$`)

const (
	defaultEncryptBatch = 50
	// defaultMaxValueBytes caps one value (the API accepts 32 KiB; Node-era rows may be larger).
	defaultMaxValueBytes = 8 << 20
	maxListedServices    = 20
)

type encryptOptions struct {
	DryRun bool // count only; write nothing
	// AllowSuspect lets the run continue when some enc:v1: values do not
	// decrypt (they are left untouched). It never overrides the wrong-key guard.
	AllowSuspect bool
	// Yes confirms a real (non-dry) run. Without it nothing is written and the
	// command exits 2.
	Yes bool
	// Target is "host:port/dbname", printed first. Never holds credentials.
	Target    string
	BatchSize int // rows per read; 0 means defaultEncryptBatch
	// MaxValueBytes: a larger value is skipped unread and counted as too_large. 0 means defaultMaxValueBytes.
	MaxValueBytes int

	// afterRead runs after each batch is read and before it is written. Tests
	// use it to simulate a user saving a value through the API in between.
	afterRead func(batch []store.EnvVar)
}

// encryptResult holds counts only. Nothing in it or in the printed output is
// ever a value, a key name or the encryption key.
type encryptResult struct {
	Scanned          int // rows read
	Encrypted        int // rows rewritten (dry run: rows that would be)
	AlreadyEncrypted int // enc:v1: rows that decrypt with the current key
	TooLarge         int // values over MaxValueBytes: skipped unread, left as they are
	Plain            int // plain-text rows seen by the read-only pre-pass
	Suspect          int // enc:v1: rows that do NOT decrypt: left alone, see below
	Conflicts        int // rows edited between read and write: left alone, a re-run picks them up
	Errors           int // rows whose update failed
}

// encryptEnv encrypts every plain-text env var value in place.
//
// It walks the whole EnvVar table in id order, one short read per batch, and
// updates each row with a compare-and-swap (UPDATE ... WHERE value = <what was
// read>), so a value saved through the API after the read is never overwritten:
// that row is counted as a conflict and left for the next run. There is no
// long transaction and no table lock; each CAS is its own statement and the
// run can be stopped and restarted at any point. Rows of soft-deleted services
// are included (harmless; teardown deletes them anyway).
//
// A value already starting with enc:v1: is never touched. If it decrypts with
// the key it counts as already encrypted. If it does not (a Node-era plaintext
// that happens to start with enc:v1:, or ciphertext made with another key) it
// is indistinguishable from a corrupt ciphertext, so it is skipped, counted as
// suspect, and the affected service ids (at most maxListedServices) are
// printed. It never fails the run and is never double-encrypted.
func encryptEnv(ctx context.Context, st *store.Store, box *secrets.Box, opt encryptOptions, out io.Writer) (encryptResult, error) {
	batch := opt.BatchSize
	if batch <= 0 {
		batch = defaultEncryptBatch
	}
	maxBytes := opt.MaxValueBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxValueBytes
	}
	suspectServices := map[string]bool{}
	tooLargeServices := map[string]bool{}
	if opt.Target != "" {
		fmt.Fprintf(out, "target: %s\n", opt.Target)
	}

	// Pass 1, read-only: classify every row before anything is written, so a
	// wrong ENV_ENCRYPTION_KEY is caught while the table is still untouched.
	var pre encryptResult
	after := ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, batch, maxBytes)
		if err != nil {
			return pre, fmt.Errorf("read env vars: %w", err)
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].ID
		for _, v := range page {
			pre.Scanned++
			switch {
			case v.TooLarge:
				pre.TooLarge++
				tooLargeServices[v.ServiceID] = true
			case !secrets.IsEncrypted(v.Value):
				pre.Plain++
			default:
				if _, err := box.Decrypt(v.Value); err != nil {
					pre.Suspect++
					suspectServices[v.ServiceID] = true
				} else {
					pre.AlreadyEncrypted++
				}
			}
		}
	}
	fmt.Fprintf(out, "pre-check: scanned=%d plain=%d already_encrypted=%d suspect=%d too_large=%d\n",
		pre.Scanned, pre.Plain, pre.AlreadyEncrypted, pre.Suspect, pre.TooLarge)
	if pre.Suspect > 0 {
		printSuspect(out, pre.Suspect, suspectServices)
		if pre.AlreadyEncrypted == 0 && pre.Plain > 0 {
			fmt.Fprintln(out, "error: no enc:v1: value decrypts and plain rows exist: this is almost certainly the wrong ENV_ENCRYPTION_KEY. Fix the key, or re-save the odd values; --allow-suspect does not override this.")
			return pre, errWrongKey
		}
		if !opt.AllowSuspect {
			fmt.Fprintln(out, "error: resolve the suspect values (see the runbook), or re-run with --allow-suspect to encrypt only the plain rows and leave these untouched.")
			return pre, errSuspect
		}
	}

	if !opt.DryRun && !opt.Yes {
		fmt.Fprintf(out, "would encrypt %d plain value(s) in %s. Nothing was changed: re-run with --dry-run to preview or --yes to apply.\n", pre.Plain, opt.Target)
		return pre, errNeedYes
	}

	// Pass 2: the write pass (or the dry-run count).
	var res encryptResult
	res.Plain = pre.Plain
	// interrupted reports a cancelled context (SIGINT/SIGTERM). Every write is
	// its own CAS, so what was written stays valid and a re-run resumes.
	interrupted := func() error {
		fmt.Fprintf(out, "interrupted: stopped before finishing (encrypted=%d so far); nothing further was written; run again to resume\n", res.Encrypted)
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	after = ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, batch, maxBytes)
		if err != nil {
			return res, fmt.Errorf("read env vars: %w", err)
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].ID
		if opt.afterRead != nil {
			opt.afterRead(page)
		}
		for _, v := range page {
			if ctx.Err() != nil {
				return res, interrupted()
			}
			res.Scanned++
			if v.TooLarge {
				res.TooLarge++
				continue
			}
			if secrets.IsEncrypted(v.Value) {
				if _, err := box.Decrypt(v.Value); err != nil {
					res.Suspect++
					suspectServices[v.ServiceID] = true
				} else {
					res.AlreadyEncrypted++
				}
				continue
			}
			if opt.DryRun {
				res.Encrypted++
				continue
			}
			enc, err := box.Encrypt(v.Value)
			if err != nil {
				res.Errors++
				fmt.Fprintf(out, "error: service %s: encrypt failed\n", v.ServiceID)
				continue
			}
			swapped, err := st.ReplaceEnvVarIfEquals(ctx, v.ServiceID, v.Key, v.Value, enc)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return res, interrupted()
				}
				res.Errors++
				// Only the service id and the SQLSTATE: a driver error text is
				// not guaranteed to be free of row data.
				fmt.Fprintf(out, "error: service %s: update failed (%s)\n", v.ServiceID, sqlState(err))
			case !swapped:
				res.Conflicts++
			default:
				res.Encrypted++
			}
		}
	}
	printSummary(out, opt.DryRun, res, suspectServices, tooLargeServices)
	if res.Errors > 0 {
		return res, fmt.Errorf("%d env var(s) could not be encrypted; fix the cause and run again (the command is safe to repeat)", res.Errors)
	}
	return res, nil
}

func sqlState(err error) string {
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return "SQLSTATE " + pgErr.Code
	}
	return "database error"
}

// printSuspect names the suspect count and at most maxListedServices service ids.
func printSuspect(out io.Writer, n int, suspect map[string]bool) {
	printIDs(out, fmt.Sprintf("warning: suspect=%d value(s) start with enc:v1: but do not decrypt with this key: a plain-text value that looks like ciphertext, or the wrong ENV_ENCRYPTION_KEY. They are NOT changed. Services:", n), suspect)
}

// printIDs prints msg followed by at most maxListedServices sorted ids.
func printIDs(out io.Writer, msg string, set map[string]bool) {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	more := ""
	if len(ids) > maxListedServices {
		ids, more = ids[:maxListedServices], fmt.Sprintf(" (+%d more)", len(ids)-maxListedServices)
	}
	fmt.Fprintf(out, "%s %v%s\n", msg, ids, more)
}

func printSummary(out io.Writer, dry bool, r encryptResult, suspect, tooLarge map[string]bool) {
	label, verb := "", "encrypted"
	if dry {
		label, verb = "dry run (nothing written): ", "would_encrypt"
	}
	fmt.Fprintf(out, "%sscanned=%d %s=%d already_encrypted=%d suspect=%d too_large=%d conflicts=%d errors=%d\n",
		label, r.Scanned, verb, r.Encrypted, r.AlreadyEncrypted, r.Suspect, r.TooLarge, r.Conflicts, r.Errors)
	if r.TooLarge > 0 {
		printIDs(out, fmt.Sprintf("warning: too_large=%d value(s) exceed the size limit and were skipped unread (still plain text if they were). Services:", r.TooLarge), tooLarge)
	}
	if r.Suspect > 0 {
		printSuspect(out, r.Suspect, suspect)
	}
	if r.Conflicts > 0 {
		fmt.Fprintf(out, "note: %d value(s) changed while the command ran and were left alone\nconflicts>0: run again until conflicts=0\n", r.Conflicts)
	}
}

// runEncryptEnv is `migrate encrypt-env [--dry-run]`. The key is validated
// before any database connection is made, so a missing or malformed
// ENV_ENCRYPTION_KEY fails fast with no writes.
func runEncryptEnv(ctx context.Context, args []string, getenv func(string) string,
	connect func(context.Context, string) (*pgxpool.Pool, func(), error), out io.Writer) error {
	fs := flag.NewFlagSet("encrypt-env", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dry := fs.Bool("dry-run", false, "count what would be encrypted; write nothing")
	yes := fs.Bool("yes", false, "confirm the real run (without it nothing is written and the exit code is 2)")
	allowSuspect := fs.Bool("allow-suspect", false, "continue although some enc:v1: values do not decrypt (they stay untouched)")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: migrate encrypt-env [--dry-run]: %w", err)
	}
	if fs.NArg() != 0 {
		return errors.New("usage: migrate encrypt-env [--dry-run]")
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if err := cfg.RequireWorker(); err != nil {
		return err
	}
	box, err := secrets.New(cfg.EnvEncryptionKey)
	if err != nil {
		return err
	}
	pool, closePool, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer closePool()
	_, err = encryptEnv(ctx, store.New(pool), box, encryptOptions{DryRun: *dry, AllowSuspect: *allowSuspect, Yes: *yes, Target: targetOf(cfg.DatabaseURL)}, out)
	return err
}
