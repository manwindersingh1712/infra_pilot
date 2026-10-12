package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/api"
	"infrapilot/internal/auth"
	"infrapilot/internal/metrics"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/workflows"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

func newBox(t *testing.T, b byte) *secrets.Box {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func setup(t *testing.T) (*store.Store, testutil.Fixture, *secrets.Box) {
	t.Helper()
	st := store.New(testutil.NeedDB(t, pool))
	return st, testutil.Seed(t, st, store.TypeNodejs), newBox(t, 9)
}

func put(t *testing.T, st *store.Store, svc, key, val string) {
	t.Helper()
	if _, err := st.UpsertEnvVar(context.Background(), svc, key, val); err != nil {
		t.Fatal(err)
	}
}

func raw(t *testing.T, st *store.Store, svc string) map[string]string {
	t.Helper()
	m, err := st.EnvMap(context.Background(), svc)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func doEncrypt(t *testing.T, st *store.Store, box *secrets.Box, opt encryptOptions) (encryptResult, string, error) {
	t.Helper()
	var out bytes.Buffer
	opt.Yes = true // the --yes gate has its own tests
	res, err := encryptEnv(context.Background(), st, box, opt, &out)
	return res, out.String(), err
}

func TestEncryptEnvIsIdempotent(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	done, _ := box.Encrypt("already")
	put(t, st, f.ServiceID, "B", done)

	res, _, err := doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != 1 || res.AlreadyEncrypted != 1 || res.Scanned != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	vars := raw(t, st, f.ServiceID)
	a, err := box.Decrypt(vars["A"])
	if err != nil || !secrets.IsEncrypted(vars["A"]) || a != "plain-a" {
		t.Fatalf("A not encrypted correctly: %v", err)
	}
	if vars["B"] != done {
		t.Fatal("an already-encrypted value was rewritten")
	}
	before := raw(t, st, f.ServiceID)
	res, _, err = doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != 0 || res.AlreadyEncrypted != 2 {
		t.Fatalf("second run res=%+v err=%v", res, err)
	}
	if after := raw(t, st, f.ServiceID); after["A"] != before["A"] || after["B"] != before["B"] {
		t.Fatal("second run changed ciphertext (double encryption)")
	}
}

func TestEncryptEnvDryRunWritesNothing(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	put(t, st, f.ServiceID, "B", "plain-b")
	res, out, err := doEncrypt(t, st, box, encryptOptions{DryRun: true})
	if err != nil || res.Encrypted != 2 || !strings.Contains(out, "dry run") {
		t.Fatalf("res=%+v err=%v out=%q", res, err, out)
	}
	vars := raw(t, st, f.ServiceID)
	if vars["A"] != "plain-a" || vars["B"] != "plain-b" {
		t.Fatalf("dry run wrote: %v", vars)
	}
}

func TestEncryptEnvDoesNotOverwriteConcurrentEdit(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "RACE", "node-era-value")
	put(t, st, f.ServiceID, "QUIET", "untouched")
	edited, _ := box.Encrypt("user-edit")
	var once atomic.Bool
	res, out, err := doEncrypt(t, st, box, encryptOptions{afterRead: func([]store.EnvVar) {
		if once.CompareAndSwap(false, true) {
			// The user saves a new value through the API between read and write.
			put(t, st, f.ServiceID, "RACE", edited)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 || res.Encrypted != 1 {
		t.Fatalf("res=%+v", res)
	}
	for _, bad := range []string{"node-era-value", "user-edit", "untouched", "RACE", edited} {
		if strings.Contains(out, bad) {
			t.Fatalf("conflict output contains %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "conflicts=1") || !strings.Contains(out, "run again until conflicts=0") {
		t.Fatalf("conflict not reported: %q", out)
	}
	vars := raw(t, st, f.ServiceID)
	if vars["RACE"] != edited {
		t.Fatalf("the concurrent edit was overwritten: %q", vars["RACE"])
	}
	if got, _ := box.Decrypt(vars["QUIET"]); got != "untouched" || !secrets.IsEncrypted(vars["QUIET"]) {
		t.Fatal("the quiet row was not encrypted")
	}
}

func TestEncryptEnvConcurrentEditToPlainIsLeftForNextRun(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "RACE", "old")
	var once atomic.Bool
	res, _, err := doEncrypt(t, st, box, encryptOptions{afterRead: func([]store.EnvVar) {
		if once.CompareAndSwap(false, true) {
			put(t, st, f.ServiceID, "RACE", "newer-plain")
		}
	}})
	if err != nil || res.Conflicts != 1 || res.Encrypted != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if raw(t, st, f.ServiceID)["RACE"] != "newer-plain" {
		t.Fatal("edit lost")
	}
	if res, _, _ := doEncrypt(t, st, box, encryptOptions{}); res.Encrypted != 1 || res.Conflicts != 0 {
		t.Fatalf("rerun res=%+v", res)
	}
}

func TestEncryptEnvEdgeValues(t *testing.T) {
	st, f, box := setup(t)
	long := strings.Repeat("0123456789abcdef", 1<<16) // 1 MiB
	cases := map[string]string{
		"EMPTY": "", "LONG": long, "UNICODE": "päss-wörd-😀-字符", "SPACES": "  lead and trail  ",
		"NEWLINE": "a\nb", "QUOTES": `a"b'c$d`,
	}
	for k, v := range cases {
		put(t, st, f.ServiceID, k, v)
	}
	res, _, err := doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != len(cases) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	vars := raw(t, st, f.ServiceID)
	for k, want := range cases {
		got, err := box.Decrypt(vars[k])
		if err != nil || got != want || !secrets.IsEncrypted(vars[k]) {
			t.Fatalf("%s did not round-trip (err=%v)", k, err)
		}
	}
}

func TestEncryptEnvManyRowsAcrossBatches(t *testing.T) {
	st, f, box := setup(t)
	const n = 1000
	for i := range n {
		put(t, st, f.ServiceID, fmt.Sprintf("K%04d", i), fmt.Sprintf("value-%d", i))
	}
	batches := 0
	res, _, err := doEncrypt(t, st, box, encryptOptions{BatchSize: 100, afterRead: func(b []store.EnvVar) {
		batches++
		if len(b) > 100 {
			t.Fatalf("batch of %d", len(b))
		}
	}})
	if err != nil || res.Encrypted != n || res.Scanned != n || batches < 10 {
		t.Fatalf("res=%+v err=%v batches=%d", res, err, batches)
	}
	vars := raw(t, st, f.ServiceID)
	for i := range n {
		if got, err := box.Decrypt(vars[fmt.Sprintf("K%04d", i)]); err != nil || got != fmt.Sprintf("value-%d", i) {
			t.Fatalf("row %d: %q %v", i, got, err)
		}
	}
	if res, _, _ := doEncrypt(t, st, box, encryptOptions{BatchSize: 100}); res.Encrypted != 0 || res.AlreadyEncrypted != n {
		t.Fatalf("rerun %+v", res)
	}
}

// A Node-era plaintext that itself starts with enc:v1: cannot be told apart
// from ciphertext. It is left alone and counted, never double-encrypted, and
// the run still succeeds. Ciphertext from another key lands in the same bucket.
func TestEncryptEnvPrefixedPlaintextIsSkippedAndCounted(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "LOOKALIKE", "enc:v1:this-was-typed-by-a-human")
	otherKey, _ := newBox(t, 4).Encrypt("other")
	put(t, st, f.ServiceID, "OTHERKEY", otherKey)
	put(t, st, f.ServiceID, "PLAIN", "p")
	// SEC-1: suspect rows need --allow-suspect, and at least one row must
	// decrypt with this key or the run is refused as a wrong-key run.
	good, _ := box.Encrypt("good")
	put(t, st, f.ServiceID, "GOOD", good)
	res, out, err := doEncrypt(t, st, box, encryptOptions{AllowSuspect: true})
	if err != nil || res.Suspect != 2 || res.Encrypted != 1 || res.Errors != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	vars := raw(t, st, f.ServiceID)
	if vars["LOOKALIKE"] != "enc:v1:this-was-typed-by-a-human" || vars["OTHERKEY"] != otherKey {
		t.Fatal("a prefixed value was rewritten")
	}
	if !strings.Contains(out, "suspect=2") || strings.Contains(out, "this-was-typed") {
		t.Fatalf("out=%q", out)
	}
}

func TestEncryptEnvIncludesSoftDeletedServices(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "LEFTOVER", "x")
	if _, err := pool.Exec(context.Background(), `UPDATE "Service" SET "deletedAt" = now() WHERE "id" = $1`, f.ServiceID); err != nil {
		t.Fatal(err)
	}
	res, _, err := doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// A failing UPDATE for one row is counted, the run carries on with the rest
// and finishes with an error (non-zero exit), and nothing sensitive is printed.
func TestEncryptEnvRowErrorsAreCountedAndFailTheRun(t *testing.T) {
	st, f, box := setup(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE FUNCTION fail_bad() RETURNS trigger AS $$ BEGIN IF OLD."key" = 'BAD' THEN RAISE EXCEPTION 'boom'; END IF; RETURN NEW; END $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER fail_bad BEFORE UPDATE ON "EnvVar" FOR EACH ROW EXECUTE FUNCTION fail_bad()`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER fail_bad ON "EnvVar"`)
		_, _ = pool.Exec(ctx, `DROP FUNCTION fail_bad()`)
	})
	put(t, st, f.ServiceID, "BAD", "bad-secret-value")
	put(t, st, f.ServiceID, "GOOD", "good-secret-value")
	res, out, err := doEncrypt(t, st, box, encryptOptions{})
	if err == nil || res.Errors != 1 || res.Encrypted != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Contains(out+err.Error(), "secret-value") {
		t.Fatalf("value leaked: %q %v", out, err)
	}
	if raw(t, st, f.ServiceID)["BAD"] != "bad-secret-value" {
		t.Fatal("BAD changed")
	}
}

func TestEncryptEnvOutputNeverContainsValuesOrKey(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "FIRST_KEY", "plain-one-sentinel")
	put(t, st, f.ServiceID, "SECOND_KEY", "päss-😀-sentinel")
	for _, dry := range []bool{true, false, false} {
		_, out, err := doEncrypt(t, st, box, encryptOptions{DryRun: dry})
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"sentinel", "FIRST_KEY", "SECOND_KEY", "enc:v1:", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))} {
			if strings.Contains(out, bad) {
				t.Fatalf("output contains %q: %q", bad, out)
			}
		}
		if !strings.Contains(out, "scanned=") {
			t.Fatalf("no summary: %q", out)
		}
	}
}

func goodKeyEnv(extra map[string]string) func(string) string {
	env := map[string]string{
		"DATABASE_URL":       "postgres://x@localhost:1/none",
		"ENV_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
	}
	for k, v := range extra {
		if v == "" {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	return func(k string) string { return env[k] }
}

func TestRunEncryptEnvFailsFastOnBadKeyWithoutConnecting(t *testing.T) {
	for name, key := range map[string]string{
		"missing": "", "not base64": "%%%not-base64%%%", "short": base64.StdEncoding.EncodeToString([]byte("tooshort")),
	} {
		t.Run(name, func(t *testing.T) {
			var connected atomic.Bool
			connect := func(context.Context, string) (*pgxpool.Pool, func(), error) {
				connected.Store(true)
				return nil, func() {}, errors.New("must not be called")
			}
			getenv := goodKeyEnv(map[string]string{"ENV_ENCRYPTION_KEY": key}) // "" removes it
			var out bytes.Buffer
			err := runEncryptEnv(context.Background(), nil, getenv, connect, &out)
			if err == nil || connected.Load() {
				t.Fatalf("err=%v connected=%v", err, connected.Load())
			}
			if strings.Contains(err.Error()+out.String(), key) && key != "" {
				t.Fatal("the key was echoed")
			}
		})
	}
}

func TestRunEncryptEnvFlagsAndDryRun(t *testing.T) {
	st, f, _ := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) { return pool, func() {}, nil }
	var out bytes.Buffer
	if err := runEncryptEnv(context.Background(), []string{"--dry-run"}, goodKeyEnv(nil), connect, &out); err != nil {
		t.Fatal(err)
	}
	if raw(t, st, f.ServiceID)["A"] != "plain-a" {
		t.Fatal("--dry-run wrote")
	}
	if err := runEncryptEnv(context.Background(), []string{"--bogus"}, goodKeyEnv(nil), connect, &out); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if err := runEncryptEnv(context.Background(), []string{"extra"}, goodKeyEnv(nil), connect, &out); err == nil {
		t.Fatal("positional argument accepted")
	}
	out.Reset()
	if err := runEncryptEnv(context.Background(), []string{"--yes"}, goodKeyEnv(nil), connect, &out); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsEncrypted(raw(t, st, f.ServiceID)["A"]) || strings.Contains(out.String(), "plain-a") {
		t.Fatalf("not encrypted or leaked: %q", out.String())
	}
	// Neither the key nor DATABASE_URL (nor its password) is ever printed.
	const pw = "s3cr3t-Pa55"
	url := "postgres://cpuser:" + pw + "@db.example:5432/appdb"
	getenv := goodKeyEnv(map[string]string{"DATABASE_URL": url})
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	for _, args := range [][]string{{"--dry-run"}, {"--yes"}, nil, {"--bogus"}, {"extra"}, {"--yes", "--allow-suspect"}} {
		var o bytes.Buffer
		err := runEncryptEnv(context.Background(), args, getenv, connect, &o)
		all := o.String()
		if err != nil {
			all += err.Error()
		}
		for _, bad := range []string{key, url, pw, "cpuser"} {
			if strings.Contains(all, bad) {
				t.Fatalf("args=%v: output contains %q: %q", args, bad, all)
			}
		}
	}
}

// The API's reveal path and the worker's credential/env loading must both read
// what encrypt-env wrote.
func TestEncryptedRowsReadBackThroughAPIAndWorker(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "NODE_ERA", "legacy-päss-1")
	if _, _, err := doEncrypt(t, st, box, encryptOptions{}); err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	h := api.NewHandler(api.Deps{Store: st, Signer: signer, Secrets: box, Metrics: metrics.NewAPI(),
		Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{"http://localhost:5173"}})
	tok, _ := signer.Sign(f.UserID)
	req := httptest.NewRequest(http.MethodGet, "/services/"+f.ServiceID+"/env?reveal=true", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var got []store.EnvVar
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got) != 1 || got[0].Value != "legacy-päss-1" {
		t.Fatalf("reveal: %d %s", rec.Code, rec.Body.String())
	}

	// Worker: a managed service whose Node-era password was plaintext. After
	// encrypt-env, EnsureCredentials must decrypt it, see it is set, and leave
	// it exactly as encrypt-env wrote it.
	rf := testutil.Seed(t, st, store.TypeRedis)
	put(t, st, rf.ServiceID, "REDIS_PASSWORD", "legacy-redis-pass")
	if _, _, err := doEncrypt(t, st, box, encryptOptions{}); err != nil {
		t.Fatal(err)
	}
	before := raw(t, st, rf.ServiceID)["REDIS_PASSWORD"]
	acts := &workflows.Activities{Store: st, Secrets: box}
	job := store.Job{ServiceID: rf.ServiceID, ServiceType: store.TypeRedis}
	if err := acts.EnsureCredentials(context.Background(), job); err != nil {
		t.Fatalf("worker could not read the encrypted password: %v", err)
	}
	if after := raw(t, st, rf.ServiceID)["REDIS_PASSWORD"]; after != before {
		t.Fatal("worker rewrote a valid encrypted password")
	}
}

// checksum fingerprints the whole EnvVar table.
func checksum(t *testing.T) string {
	t.Helper()
	var sum *string
	err := pool.QueryRow(context.Background(),
		`SELECT md5(string_agg("id" || '|' || "key" || '|' || "value", ',' ORDER BY "id")) FROM "EnvVar"`).Scan(&sum)
	if err != nil {
		t.Fatal(err)
	}
	if sum == nil {
		return "empty"
	}
	return *sum
}

// A wrong ENV_ENCRYPTION_KEY: rows the right key wrote do not decrypt. Nothing
// may be written, or the plain rows would be locked under the wrong key.
func TestEncryptEnvWrongKeyWritesNothing(t *testing.T) {
	st, f, wrong := setup(t) // key 9 is the "wrong" one here
	right := newBox(t, 4)
	put(t, st, f.ServiceID, "PLAIN1", "p1")
	put(t, st, f.ServiceID, "PLAIN2", "p2")
	for _, k := range []string{"GO1", "GO2"} {
		enc, _ := right.Encrypt("v-" + k)
		put(t, st, f.ServiceID, k, enc)
	}
	before := checksum(t)
	for _, allow := range []bool{false, true} {
		res, out, err := doEncrypt(t, st, wrong, encryptOptions{AllowSuspect: allow})
		if !errors.Is(err, errWrongKey) {
			t.Fatalf("allow=%v: want errWrongKey, got %v", allow, err)
		}
		if res.Encrypted != 0 || !strings.Contains(out, "suspect=2") || !strings.Contains(out, f.ServiceID) {
			t.Fatalf("allow=%v res=%+v out=%q", allow, res, out)
		}
		if checksum(t) != before {
			t.Fatalf("allow=%v: the table changed", allow)
		}
	}
	// The same table with the right key is fine.
	if _, _, err := doEncrypt(t, st, right, encryptOptions{DryRun: true}); err != nil {
		t.Fatalf("right key refused: %v", err)
	}
}

// Mixed ok and suspect: refused without --allow-suspect, proceeds with it and
// then encrypts only the plain rows.
func TestEncryptEnvSuspectNeedsAllowFlag(t *testing.T) {
	st, f, box := setup(t)
	good, _ := box.Encrypt("good")
	put(t, st, f.ServiceID, "GOOD", good)
	put(t, st, f.ServiceID, "LOOKALIKE", "enc:v1:typed-by-a-human")
	put(t, st, f.ServiceID, "PLAIN", "p")
	before := checksum(t)

	res, out, err := doEncrypt(t, st, box, encryptOptions{})
	if !errors.Is(err, errSuspect) || res.Encrypted != 0 || checksum(t) != before {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(out, "suspect=1") || !strings.Contains(out, "--allow-suspect") || strings.Contains(out, "typed-by") {
		t.Fatalf("out=%q", out)
	}

	res, out, err = doEncrypt(t, st, box, encryptOptions{AllowSuspect: true})
	if err != nil || res.Encrypted != 1 || res.Suspect != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.Contains(out, "warning") {
		t.Fatalf("suspect warning missing with the flag: %q", out)
	}
	vars := raw(t, st, f.ServiceID)
	if vars["LOOKALIKE"] != "enc:v1:typed-by-a-human" || !secrets.IsEncrypted(vars["PLAIN"]) {
		t.Fatalf("vars=%v", vars)
	}
}

// Lookalike only (no row decrypts) next to plain rows is the wrong-key shape:
// refused even with --allow-suspect.
func TestEncryptEnvAllSuspectWithPlainRefusedEvenWithFlag(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "LOOKALIKE", "enc:v1:typed-by-a-human")
	put(t, st, f.ServiceID, "PLAIN", "p")
	before := checksum(t)
	for _, allow := range []bool{false, true} {
		_, out, err := doEncrypt(t, st, box, encryptOptions{AllowSuspect: allow})
		if !errors.Is(err, errWrongKey) || checksum(t) != before {
			t.Fatalf("allow=%v err=%v", allow, err)
		}
		if !strings.Contains(out, "ENV_ENCRYPTION_KEY") {
			t.Fatalf("message does not name the key: %q", out)
		}
	}
	// Without plain rows there is nothing to lock under a wrong key: allowed.
	if _, err := pool.Exec(context.Background(), `DELETE FROM "EnvVar" WHERE "key" = 'PLAIN'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := doEncrypt(t, st, box, encryptOptions{AllowSuspect: true}); err != nil {
		t.Fatalf("suspect-only table with the flag: %v", err)
	}
}

func TestEncryptEnvNoEncryptedRowsProceeds(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "A", "a")
	put(t, st, f.ServiceID, "B", "b")
	res, _, err := doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// --dry-run applies the same checks and never writes.
func TestEncryptEnvDryRunAppliesSuspectChecks(t *testing.T) {
	st, f, box := setup(t)
	good, _ := box.Encrypt("good")
	put(t, st, f.ServiceID, "GOOD", good)
	put(t, st, f.ServiceID, "LOOKALIKE", "enc:v1:typed")
	put(t, st, f.ServiceID, "PLAIN", "p")
	before := checksum(t)
	if _, _, err := doEncrypt(t, st, box, encryptOptions{DryRun: true}); !errors.Is(err, errSuspect) {
		t.Fatalf("dry run did not refuse suspect rows: %v", err)
	}
	res, _, err := doEncrypt(t, st, box, encryptOptions{DryRun: true, AllowSuspect: true})
	if err != nil || res.Encrypted != 1 || res.Suspect != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if checksum(t) != before {
		t.Fatal("dry run wrote")
	}
	// Wrong key shape in a dry run.
	if _, err := pool.Exec(context.Background(), `DELETE FROM "EnvVar" WHERE "key" = 'GOOD'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := doEncrypt(t, st, box, encryptOptions{DryRun: true, AllowSuspect: true}); !errors.Is(err, errWrongKey) {
		t.Fatalf("dry run did not apply the wrong-key guard: %v", err)
	}
}

func TestSuspectServiceListIsCapped(t *testing.T) {
	ids := map[string]bool{}
	for i := range 25 {
		ids[fmt.Sprintf("svc%02d", i)] = true
	}
	var out bytes.Buffer
	printSuspect(&out, 25, ids)
	s := out.String()
	if !strings.Contains(s, "svc19") || strings.Contains(s, "svc20") || !strings.Contains(s, "+5 more") {
		t.Fatalf("out=%q", s)
	}
}

func TestEncryptEnvConflictsLastLine(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "RACE", "old")
	var once atomic.Bool
	_, out, err := doEncrypt(t, st, box, encryptOptions{afterRead: func([]store.EnvVar) {
		if once.CompareAndSwap(false, true) {
			put(t, st, f.ServiceID, "RACE", "newer")
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "conflicts>0: run again until conflicts=0") {
		t.Fatalf("last line %q", last)
	}
}

// The command-level flags: suspect rows are refused by default and accepted
// with --allow-suspect.
func TestRunEncryptEnvAllowSuspectFlag(t *testing.T) {
	st, f, box := setup(t)
	good, _ := box.Encrypt("good")
	put(t, st, f.ServiceID, "GOOD", good)
	put(t, st, f.ServiceID, "LOOKALIKE", "enc:v1:typed")
	put(t, st, f.ServiceID, "PLAIN", "p")
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) { return pool, func() {}, nil }
	var out bytes.Buffer
	if err := runEncryptEnv(context.Background(), nil, goodKeyEnv(nil), connect, &out); !errors.Is(err, errSuspect) {
		t.Fatalf("err=%v", err)
	}
	if raw(t, st, f.ServiceID)["PLAIN"] != "p" {
		t.Fatal("wrote despite suspect rows")
	}
	if err := runEncryptEnv(context.Background(), []string{"--allow-suspect", "--yes"}, goodKeyEnv(nil), connect, &out); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsEncrypted(raw(t, st, f.ServiceID)["PLAIN"]) {
		t.Fatal("--allow-suspect did not proceed")
	}
}

// pgxTarget renders what pgx resolves for dsn, independently of targetOf.
func pgxTarget(t *testing.T, dsn string) string {
	t.Helper()
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx rejects %q: %v", dsn, err)
	}
	host := cfg.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d/%s", host, cfg.Port, cfg.Database)
}

// The target line is what pgx will really connect to: query parameters,
// key=value DSNs, unix sockets, IPv6 and multi-host lists included.
func TestTargetOfMatchesWhatPgxResolves(t *testing.T) {
	cases := []struct{ in, want string }{
		{"postgresql://cp:cp@localhost:5432/control_plane?schema=public", "localhost:5432/control_plane"},
		{"postgres://u:pw@db.internal/prod", "db.internal:5432/prod"},
		{"postgres://u:p@localhost/db?host=/tmp/sock", "/tmp/sock:5432/db"},
		{"postgres://u:p@localhost:5432/db?dbname=other&port=6000", "localhost:6000/other"},
		{"postgres://u:p@localhost:5432/db?host=other.example", "other.example:5432/db"},
		{"host=h password=SECRET dbname=d user=zzuser", "h:5432/d"},
		{"host=/var/run/postgresql dbname=d port=5555 user=zzuser password=SECRET", "/var/run/postgresql:5555/d"},
		{"postgres://u:p@[::1]:5433/app", "[::1]:5433/app"},
		{"postgres://u:p@a:1,b:2/db?sslmode=disable", "a:1,b:2/db"},
		{"postgres://u:p@a,b:2/db?sslmode=disable", "a:5432,b:2/db"},
		{"postgres://cp:p%40ss%2Fw%3Frd%23x%2540@db.example:6543/app?sslmode=disable&password=q-secret", "db.example:6543/app"},
	}
	for _, c := range cases {
		got := targetOf(c.in)
		if got != c.want {
			t.Errorf("targetOf(%q) = %q, want %q", c.in, got, c.want)
		}
		if !strings.Contains(c.in, ",") && got != pgxTarget(t, c.in) {
			t.Errorf("targetOf(%q) = %q disagrees with pgx %q", c.in, got, pgxTarget(t, c.in))
		}
		for _, bad := range []string{"zzuser", "SECRET", "q-secret", "p@ss", "p%40ss", "sslmode", "pw", "cp:"} {
			if strings.Contains(got, bad) {
				t.Errorf("targetOf(%q) = %q leaks %q", c.in, got, bad)
			}
		}
	}
}

func TestTargetOfUsesPGEnvDefaults(t *testing.T) {
	t.Setenv("PGHOST", "envhost.example")
	t.Setenv("PGPORT", "7777")
	if got, want := targetOf("postgres:///db"), "envhost.example:7777/db"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTargetOfUnknown(t *testing.T) {
	for _, in := range []string{
		"", "://nope", "postgres://u:p%zz@h/db", "postgres://cp:pw@/", "postgres://cp:pw@host/db name;drop",
		"postgres://u:pw@host", "host=h password=hunter2 port=notaport",
		"postgres://u:12/pw#x@h/db",
	} {
		got := targetOf(in)
		if got == unknownTarget {
			continue
		}
		// If pgx can resolve it, the answer must be the true one and carry no credential text.
		if strings.Contains(got, "hunter2") || strings.Contains(got, "pw") || strings.Contains(got, "12/") {
			t.Errorf("targetOf(%q) = %q", in, got)
		}
		if _, err := pgconn.ParseConfig(in); err != nil {
			t.Errorf("targetOf(%q) = %q but pgx cannot parse it", in, got)
		}
	}
	if got := targetOf("postgres://u:p%zz@h/db"); got != unknownTarget {
		t.Fatalf("got %q", got)
	}
}

// With an undeterminable target a real run is refused (exit 2) with or without
// --yes; a dry run may continue.
func TestRunEncryptEnvUnknownTargetRefusesRealRun(t *testing.T) {
	st, f, _ := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	var connects atomic.Int32
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) {
		connects.Add(1)
		return pool, func() {}, nil
	}
	getenv := goodKeyEnv(map[string]string{"DATABASE_URL": "postgres://u:p%zz@h/db"})
	for _, args := range [][]string{nil, {"--yes"}, {"--yes", "--allow-suspect"}} {
		var out bytes.Buffer
		err := runEncryptEnv(context.Background(), args, getenv, connect, &out)
		if connects.Load() != 0 {
			t.Fatal("connected although the target is unknown")
		}
		if exitCode(err) != 2 || !strings.Contains(out.String(), "target: (unknown target)") {
			t.Fatalf("args=%v err=%v out=%q", args, err, out.String())
		}
		if raw(t, st, f.ServiceID)["A"] != "plain-a" {
			t.Fatal("wrote with an unknown target")
		}
	}
	var out bytes.Buffer
	if err := runEncryptEnv(context.Background(), []string{"--dry-run"}, getenv, connect, &out); err != nil {
		t.Fatalf("dry run refused: %v", err)
	}
	if !strings.Contains(out.String(), "target: (unknown target)") || !strings.Contains(out.String(), "would_encrypt=1") {
		t.Fatalf("out=%q", out.String())
	}
	// The same guard sits inside encryptEnv.
	box := newBox(t, 9)
	if _, err := encryptEnv(context.Background(), st, box, encryptOptions{Yes: true, Target: unknownTarget}, &out); exitCode(err) != 2 {
		t.Fatalf("encryptEnv err=%v", err)
	}
}

func TestEncryptEnvPrintsTargetAndCountsFirst(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "A", "a")
	_, out, err := doEncrypt(t, st, box, encryptOptions{DryRun: true, Target: "db.example:5432/app"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out, "\n")
	if lines[0] != "target: db.example:5432/app" || !strings.HasPrefix(lines[1], "pre-check: scanned=1 plain=1") {
		t.Fatalf("out=%q", out)
	}
}

// The real run needs --yes. Without it nothing is written, the plan is
// printed and the exit code is 2.
func TestEncryptEnvRealRunNeedsYes(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "A", "a")
	before := checksum(t)
	var out bytes.Buffer
	res, err := encryptEnv(context.Background(), st, box, encryptOptions{Target: "h:1/d"}, &out)
	if exitCode(err) != 2 || res.Encrypted != 0 || checksum(t) != before {
		t.Fatalf("err=%v code=%d res=%+v", err, exitCode(err), res)
	}
	for _, want := range []string{"would encrypt 1", "--dry-run", "--yes", "h:1/d"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %q", want, out.String())
		}
	}
	// Dry run does not need it; --yes does the work.
	if _, err := encryptEnv(context.Background(), st, box, encryptOptions{DryRun: true}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := encryptEnv(context.Background(), st, box, encryptOptions{Yes: true}, &out); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsEncrypted(raw(t, st, f.ServiceID)["A"]) {
		t.Fatal("--yes did not write")
	}
}

func TestExitCodes(t *testing.T) {
	if exitCode(nil) != 0 || exitCode(errors.New("x")) != 1 || exitCode(errSuspect) != 1 ||
		exitCode(fmt.Errorf("wrap: %w", errNeedYes)) != 2 {
		t.Fatal("exit codes")
	}
}

// The credentials in DATABASE_URL (password with special characters) and the
// key never reach the output, on any path.
func TestRunEncryptEnvNeverPrintsCredentials(t *testing.T) {
	st, f, _ := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	const pw = "p%40ss%3Aw%2Frd%23"
	const rawPw = "p@ss:w/rd#"
	url := "postgres://cpuser:" + pw + "@db.example:6543/appdb?sslmode=disable&application_name=zzsecretapp"
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) { return pool, func() {}, nil }
	getenv := goodKeyEnv(map[string]string{"DATABASE_URL": url})
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	for _, args := range [][]string{{"--dry-run"}, nil, {"--yes"}, {"--yes"}} {
		var out bytes.Buffer
		err := runEncryptEnv(context.Background(), args, getenv, connect, &out)
		if exitCode(err) > 2 {
			t.Fatal(err)
		}
		all := out.String()
		if err != nil {
			all += err.Error()
		}
		for _, bad := range []string{pw, rawPw, "p@ss", "cpuser", "zzsecretapp", "sslmode", url, key, "plain-a", "postgres://"} {
			if strings.Contains(all, bad) {
				t.Fatalf("args=%v: output contains %q: %q", args, bad, all)
			}
		}
		if !strings.Contains(all, "target: db.example:6543/appdb") {
			t.Fatalf("no target line: %q", all)
		}
	}
}

func TestRunWithoutYesExitsTwoAndWritesNothing(t *testing.T) {
	st, f, _ := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) { return pool, func() {}, nil }
	var out bytes.Buffer
	err := runEncryptEnv(context.Background(), nil, goodKeyEnv(nil), connect, &out)
	if exitCode(err) != 2 || raw(t, st, f.ServiceID)["A"] != "plain-a" {
		t.Fatalf("err=%v", err)
	}
}

func TestDotenvNote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.env", []byte("DATABASE_URL=postgres://u:topsecret@h/d\nZZ_SENTINEL_VAR=hush\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var out bytes.Buffer
	loadDotenv(&out)
	got := out.String()
	if !strings.Contains(got, ".env") || !strings.Contains(got, filepath.Base(dir)) || strings.Contains(got, "topsecret") || strings.Contains(got, "hush") {
		t.Fatalf("got %q", got)
	}
	_ = os.Unsetenv("ZZ_SENTINEL_VAR")
	_ = os.Unsetenv("DATABASE_URL")
	empty := t.TempDir()
	t.Chdir(empty)
	out.Reset()
	loadDotenv(&out)
	if out.Len() != 0 {
		t.Fatalf("printed without a .env: %q", out.String())
	}
}

// Through realMain with a real connection string: no --yes exits 2 and writes
// nothing, --yes exits 0.
func TestRealMainEncryptEnvExit2ThenZero(t *testing.T) {
	st, f, _ := setup(t)
	put(t, st, f.ServiceID, "A", "plain-a")
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", pool.Config().ConnString())
	t.Setenv("ENV_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	var out, errb bytes.Buffer
	if code := realMain(context.Background(), []string{"encrypt-env"}, &out, &errb); code != 2 {
		t.Fatalf("code %d out=%q err=%q", code, out.String(), errb.String())
	}
	if raw(t, st, f.ServiceID)["A"] != "plain-a" {
		t.Fatal("wrote without --yes")
	}
	if code := realMain(context.Background(), []string{"encrypt-env", "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("code %d err=%q", code, errb.String())
	}
	if !secrets.IsEncrypted(raw(t, st, f.ServiceID)["A"]) {
		t.Fatal("not encrypted")
	}
}

// A value above the cap is never loaded: it is skipped, counted as too_large
// (id only), left as it is, and the other rows are still processed.
func TestEncryptEnvSkipsTooLargeValues(t *testing.T) {
	st, f, box := setup(t)
	big := strings.Repeat("Z", 101)
	put(t, st, f.ServiceID, "BIG", big)
	put(t, st, f.ServiceID, "EXACT", strings.Repeat("y", 100))
	put(t, st, f.ServiceID, "SMALL", "s")
	// Dry run reports it too and still never writes.
	res, _, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 100, DryRun: true})
	if err != nil || res.TooLarge != 1 || res.Encrypted != 2 || raw(t, st, f.ServiceID)["SMALL"] != "s" {
		t.Fatalf("dry res=%+v err=%v", res, err)
	}
	res, out, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 100})
	if err != nil || res.TooLarge != 1 || res.Encrypted != 2 {
		t.Fatalf("res=%+v err=%v out=%q", res, err, out)
	}
	vars := raw(t, st, f.ServiceID)
	if vars["BIG"] != big || !secrets.IsEncrypted(vars["EXACT"]) || !secrets.IsEncrypted(vars["SMALL"]) {
		t.Fatal("wrong rows rewritten")
	}
	var pre, sum bool
	for _, l := range strings.Split(out, "\n") {
		pre = pre || (strings.HasPrefix(l, "pre-check:") && strings.Contains(l, "too_large=1"))
		sum = sum || (strings.HasPrefix(l, "scanned=") && strings.Contains(l, "too_large=1"))
	}
	if !pre || !sum || !strings.Contains(out, f.ServiceID) || strings.Contains(out, "ZZZ") || strings.Contains(out, "BIG") {
		t.Fatalf("out=%q", out)
	}
}

func TestEncryptBatchDefaultsAreBounded(t *testing.T) {
	if defaultEncryptBatch != 50 || defaultMaxValueBytes != 8<<20 {
		t.Fatalf("batch=%d max=%d", defaultEncryptBatch, defaultMaxValueBytes)
	}
}

// Cancelling the context (SIGINT/SIGTERM in the real binary) stops the run
// before the next write, returns a non-zero error, and a re-run finishes.
func TestEncryptEnvCancelStopsCleanlyAndResumes(t *testing.T) {
	st, f, box := setup(t)
	for i := range 30 {
		put(t, st, f.ServiceID, fmt.Sprintf("K%02d", i), fmt.Sprintf("v%d", i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	res, err := encryptEnv(ctx, st, box, encryptOptions{Yes: true, BatchSize: 10, afterRead: func([]store.EnvVar) { cancel() }}, &out)
	if !errors.Is(err, context.Canceled) || exitCode(err) != 1 || res.Encrypted != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for k, v := range raw(t, st, f.ServiceID) {
		if secrets.IsEncrypted(v) {
			t.Fatalf("%s was written after the cancel", k)
		}
	}
	if !strings.Contains(out.String(), "interrupted") || strings.Contains(out.String(), "v1") {
		t.Fatalf("out=%q", out.String())
	}
	res, _, err = doEncrypt(t, st, box, encryptOptions{BatchSize: 10})
	if err != nil || res.Encrypted != 30 {
		t.Fatalf("resume res=%+v err=%v", res, err)
	}
}

func TestNotifyContextCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := notifyContext(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel the context")
	}
}
