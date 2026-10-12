package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/config"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
)

const (
	defaultEncryptBatch = 200
	maxListedServices   = 20
)

type encryptOptions struct {
	DryRun    bool // count only; write nothing
	BatchSize int  // rows per read; 0 means defaultEncryptBatch

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
	var res encryptResult
	suspectServices := map[string]bool{}
	after := ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, batch)
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
			res.Scanned++
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
					return res, ctx.Err()
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
	printSummary(out, opt.DryRun, res, suspectServices)
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

func printSummary(out io.Writer, dry bool, r encryptResult, suspect map[string]bool) {
	label, verb := "", "encrypted"
	if dry {
		label, verb = "dry run (nothing written): ", "would_encrypt"
	}
	fmt.Fprintf(out, "%sscanned=%d %s=%d already_encrypted=%d suspect=%d conflicts=%d errors=%d\n",
		label, r.Scanned, verb, r.Encrypted, r.AlreadyEncrypted, r.Suspect, r.Conflicts, r.Errors)
	if r.Suspect > 0 {
		ids := make([]string, 0, len(suspect))
		for id := range suspect {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		more := ""
		if len(ids) > maxListedServices {
			ids, more = ids[:maxListedServices], fmt.Sprintf(" (+%d more)", len(ids)-maxListedServices)
		}
		fmt.Fprintf(out, "warning: %d value(s) start with enc:v1: but do not decrypt with this key: a plain-text value that looks like ciphertext, or the wrong ENV_ENCRYPTION_KEY. They were NOT changed. Services: %v%s\n",
			r.Suspect, ids, more)
	}
	if r.Conflicts > 0 {
		fmt.Fprintf(out, "note: %d value(s) changed while the command ran and were left alone; run it again to pick them up\n", r.Conflicts)
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
	_, err = encryptEnv(ctx, store.New(pool), box, encryptOptions{DryRun: *dry}, out)
	return err
}
