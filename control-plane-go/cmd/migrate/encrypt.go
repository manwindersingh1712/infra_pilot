package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/config"
	"infrapilot/internal/db"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
)

var (
	// errSuspect: enc:v1: values that do not decrypt, and --allow-suspect was not given.
	errSuspect = errors.New("refusing to write: some enc:v1: values do not decrypt with this key (nothing was changed)")
	// errNeedYes: a real run without --yes. Exit code 2.
	errNeedYes = &exitCodeError{code: 2, msg: "not confirmed: nothing was changed; use --dry-run to preview or --yes to apply"}
	// errUnknownTarget: a real run whose database target cannot be shown. Exit code 2.
	errUnknownTarget = &exitCodeError{code: 2, msg: "refusing a real run: the database target cannot be determined (nothing was changed); fix DATABASE_URL or use --dry-run"}
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

const unknownTarget = "(unknown target)"

// targetOf renders what pgx will really connect to ("host:port/dbname", see
// db.Target). It never returns the user, the password or a query value; when
// the target cannot be determined it returns unknownTarget.
func targetOf(raw string) string {
	if t, ok := db.Target(raw); ok {
		return t
	}
	return unknownTarget
}

const (
	defaultEncryptBatch = 50
	// defaultMaxValueBytes caps one PLAINTEXT value (the API accepts 32 KiB; Node-era
	// rows may be larger). An oversize value that starts with enc:v1: is ciphertext
	// (about 4/3 of its plaintext) and is counted as already_encrypted_large.
	defaultMaxValueBytes = 8 << 20
	// defaultMaxBatchBytes caps the summed value sizes of one read batch, so
	// peak memory is about this, not batch x defaultMaxValueBytes.
	defaultMaxBatchBytes = 16 << 20
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
	// MaxBatchBytes: a batch stops filling once its value sizes would pass this. 0 means defaultMaxBatchBytes.
	MaxBatchBytes int

	// afterRead runs after each batch is read and before it is written. Tests
	// use it to simulate a user saving a value through the API in between.
	afterRead func(batch []store.EnvVar)
	// replace is the compare-and-swap write; nil means st.ReplaceEnvVarIfEquals.
	// Tests replace it to simulate a signal arriving while the UPDATE is in flight.
	replace func(ctx context.Context, serviceID, key, oldValue, newValue string) (bool, error)
}

// encryptResult holds counts only. Nothing in it or in the printed output is
// ever a value, a key name or the encryption key.
type encryptResult struct {
	Scanned          int // rows read
	Encrypted        int // rows rewritten (dry run: rows that would be)
	AlreadyEncrypted int // enc:v1: rows that decrypt with the current key
	// AlreadyEncryptedLarge counts oversize enc:v1: rows (ciphertext of a large
	// plaintext). They are never loaded and NOT decrypt-verified, and they are
	// included in neither AlreadyEncrypted nor Suspect.
	AlreadyEncryptedLarge int
	TooLarge              int // values over MaxValueBytes: skipped unread, left as they are
	Plain                 int // plain-text rows seen by the read-only pre-pass
	Suspect               int // enc:v1: rows that do NOT decrypt: left alone, see below
	Conflicts             int // rows edited between read and write: left alone, a re-run picks them up
	Errors                int // rows whose update failed
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
	maxBatch := opt.MaxBatchBytes
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatchBytes
	}
	suspectServices := map[string]bool{}
	tooLargeServices := map[string]bool{}
	if opt.Target != "" {
		fmt.Fprintf(out, "target: %s\n", opt.Target)
	}
	if opt.Target == unknownTarget && !opt.DryRun {
		return encryptResult{}, errUnknownTarget
	}

	var res encryptResult
	// interrupted reports a cancelled context (SIGINT/SIGTERM), in a read or a
	// write. Every write is its own CAS, so what was written stays valid and a
	// re-run resumes. unverified marks an in-flight write whose outcome is unknown.
	unverified := false
	interrupted := func() error {
		note := ""
		if unverified {
			note = " (the write in flight at the signal could not be verified)"
		}
		fmt.Fprintf(out, "interrupted: stopped before finishing (encrypted=%d so far%s); nothing further was written; run again to resume\n", res.Encrypted, note)
		return fmt.Errorf("interrupted: %w", ctx.Err())
	}
	replace := opt.replace
	if replace == nil {
		replace = st.ReplaceEnvVarIfEquals
	}

	// Pass 1, read-only: classify every row before anything is written, so a
	// wrong ENV_ENCRYPTION_KEY is caught while the table is still untouched.
	var pre encryptResult
	after := ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, batch, maxBytes, maxBatch)
		if err != nil {
			if ctx.Err() != nil {
				return pre, interrupted()
			}
			return pre, fmt.Errorf("read env vars: %w", err)
		}
		if len(page) == 0 {
			break
		}
		after = page[len(page)-1].ID
		for _, v := range page {
			pre.Scanned++
			switch {
			case v.EncryptedLarge:
				pre.AlreadyEncryptedLarge++
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
	fmt.Fprintf(out, "pre-check: scanned=%d plain=%d already_encrypted=%d already_encrypted_large=%d suspect=%d too_large=%d\n",
		pre.Scanned, pre.Plain, pre.AlreadyEncrypted, pre.AlreadyEncryptedLarge, pre.Suspect, pre.TooLarge)
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
	res.Plain = pre.Plain
	after = ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, batch, maxBytes, maxBatch)
		if err != nil {
			if ctx.Err() != nil {
				return res, interrupted()
			}
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
			if v.EncryptedLarge {
				res.AlreadyEncryptedLarge++
				continue
			}
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
			swapped, err := replace(ctx, v.ServiceID, v.Key, v.Value, enc)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					// The UPDATE may have committed before the cancel reached
					// it: ask the database (on a fresh context) so the count is exact.
					switch committed, verr := inflightCommitted(ctx, st, v.ServiceID, v.Key, enc); {
					case verr != nil:
						unverified = true
					case committed:
						res.Encrypted++
					}
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
	var errs []error
	if res.Errors > 0 {
		errs = append(errs, fmt.Errorf("%d env var(s) could not be encrypted; fix the cause and run again (the command is safe to repeat)", res.Errors))
	}
	if res.TooLarge > 0 {
		// Every other row is done; the run still fails so a sign-off cannot pass with plaintext left.
		errs = append(errs, fmt.Errorf("too_large=%d value(s) over the size limit were skipped and never encrypted: inspect them and delete or re-save them through the API (services %s)",
			res.TooLarge, idList(tooLargeServices)))
	}
	return res, errors.Join(errs...)
}

// connectFailure replaces a connect/ping error with a fixed message, the error
// class and the safe target. The driver text names the DB user and can echo
// URL fragments, so it is never printed.
func connectFailure(target string, err error) error {
	// db.Connect's own parse errors are fixed texts without input.
	if strings.HasPrefix(err.Error(), "parse DATABASE_URL") {
		return errors.New(err.Error())
	}
	class := "connection error"
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		class = "interrupted"
	case errors.Is(err, context.DeadlineExceeded):
		class = "timeout"
	case errors.As(err, &netErr):
		class = "network error"
	}
	if _, ok := errors.AsType[*pgconn.PgError](err); ok {
		class = sqlState(err)
	}
	return fmt.Errorf("cannot connect to the database at %s (%s); check the target, user, password and database name in DATABASE_URL", target, class)
}

// inflightCommitted reports whether the row now holds enc (the ciphertext
// just produced, unique because of its random nonce). It uses a fresh context
// because the run's one is cancelled.
func inflightCommitted(ctx context.Context, st *store.Store, serviceID, key, enc string) (bool, error) {
	vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	m, err := st.EnvMap(vctx, serviceID)
	if err != nil {
		return false, err
	}
	return m[key] == enc, nil
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
	fmt.Fprintf(out, "%s %s\n", msg, idList(set))
}

// idList renders at most maxListedServices sorted ids, "[a b] (+n more)".
func idList(set map[string]bool) string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	more := ""
	if len(ids) > maxListedServices {
		ids, more = ids[:maxListedServices], fmt.Sprintf(" (+%d more)", len(ids)-maxListedServices)
	}
	return fmt.Sprintf("%v%s", ids, more)
}

func printSummary(out io.Writer, dry bool, r encryptResult, suspect, tooLarge map[string]bool) {
	label, verb := "", "encrypted"
	if dry {
		label, verb = "dry run (nothing written): ", "would_encrypt"
	}
	fmt.Fprintf(out, "%sscanned=%d %s=%d already_encrypted=%d already_encrypted_large=%d suspect=%d too_large=%d conflicts=%d errors=%d\n",
		label, r.Scanned, verb, r.Encrypted, r.AlreadyEncrypted, r.AlreadyEncryptedLarge, r.Suspect, r.TooLarge, r.Conflicts, r.Errors)
	if r.TooLarge > 0 {
		printIDs(out, fmt.Sprintf("warning: too_large=%d value(s) exceed the size limit and were skipped unread (still plain text if they were). Services:", r.TooLarge), tooLarge)
	}
	if r.AlreadyEncryptedLarge > 0 {
		fmt.Fprintf(out, "note: already_encrypted_large=%d value(s) over the size limit start with enc:v1: and were NOT loaded or decrypt-verified (assumed to be ciphertext).\n", r.AlreadyEncryptedLarge)
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
		return fmt.Errorf("usage: migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]: %w", err)
	}
	if fs.NArg() != 0 {
		return errors.New("usage: migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]")
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
	target := targetOf(cfg.DatabaseURL)
	if target == unknownTarget && !*dry {
		fmt.Fprintf(out, "target: %s\n", target)
		return errUnknownTarget
	}
	pool, closePool, err := connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return connectFailure(target, err)
	}
	defer closePool()
	_, err = encryptEnv(ctx, store.New(pool), box, encryptOptions{DryRun: *dry, AllowSuspect: *allowSuspect, Yes: *yes, Target: target}, out)
	return err
}
