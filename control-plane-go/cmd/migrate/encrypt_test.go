package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	// Dry run reports it too, still never writes, and exits 1 after finishing every row.
	res, _, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 100, DryRun: true})
	if exitCode(err) != 1 || res.TooLarge != 1 || res.Encrypted != 2 || res.Scanned != 3 || raw(t, st, f.ServiceID)["SMALL"] != "s" {
		t.Fatalf("dry res=%+v err=%v", res, err)
	}
	res, out, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 100})
	if exitCode(err) != 1 || res.TooLarge != 1 || res.Encrypted != 2 {
		t.Fatalf("res=%+v err=%v out=%q", res, err, out)
	}
	// The final error names the service id (no key, no value) and what to do.
	for _, want := range []string{"too_large=1", f.ServiceID, "re-save"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "BIG") || strings.Contains(err.Error(), "ZZZ") {
		t.Fatalf("error leaks: %q", err)
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
	// With the cap raised nothing is too large and the run exits 0.
	if res, _, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 1000}); err != nil || res.TooLarge != 0 || res.Encrypted != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestEncryptBatchDefaultsAreBounded(t *testing.T) {
	if defaultEncryptBatch != 50 || defaultMaxValueBytes != 8<<20 || defaultMaxBatchBytes != 16<<20 {
		t.Fatalf("batch=%d max=%d bytes=%d", defaultEncryptBatch, defaultMaxValueBytes, defaultMaxBatchBytes)
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

// Connect and ping errors from pgx name the DB user and can echo URL
// fragments. The command prints a fixed message, the error class and the
// safe target only.
func TestRunEncryptEnvConnectErrorsAreRedacted(t *testing.T) {
	cc := pool.Config().ConnConfig
	host, port, dbname, realUser, realPw := cc.Host, cc.Port, cc.Database, cc.User, cc.Password
	const user, pw, mark = "zzdbuser", "ZZPWMARKER", "zzdbmarker"
	cases := map[string]struct{ url, class, target string }{
		"bad password":     {fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable&application_name=zzappmark", user, pw, host, port, dbname), "SQLSTATE 28", fmt.Sprintf("%s:%d/%s", host, port, dbname)},
		"wrong password":   {fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", realUser, pw, host, port, dbname), "SQLSTATE 28P01", fmt.Sprintf("%s:%d/%s", host, port, dbname)},
		"bad host":         {fmt.Sprintf("postgres://%s:%s@127.0.0.1:1/%s?sslmode=disable", user, pw, dbname), "network error", "127.0.0.1:1/" + dbname},
		"bad dbname":       {fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", realUser, realPw, host, port, mark), "SQLSTATE 3D000", fmt.Sprintf("%s:%d/%s", host, port, mark)},
		"password in path": {fmt.Sprintf("postgres://%s@%s:%d/%s?password=%s&sslmode=disable", realUser, host, port, mark, pw), "SQLSTATE 28P01", fmt.Sprintf("%s:%d/%s", host, port, mark)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			getenv := goodKeyEnv(map[string]string{"DATABASE_URL": c.url})
			err := runEncryptEnv(context.Background(), []string{"--dry-run"}, getenv, connectDB, &out)
			if err == nil || exitCode(err) != 1 {
				t.Fatalf("err=%v", err)
			}
			all := out.String() + err.Error()
			for _, bad := range []string{user, pw, "zzappmark", "user=", "password=", "database \"", "SASL", "FATAL"} {
				if bad != "" && strings.Contains(all, bad) {
					t.Fatalf("output contains %q: %q", bad, all)
				}
			}
			if !strings.Contains(err.Error(), "cannot connect") || !strings.Contains(err.Error(), c.class) || !strings.Contains(err.Error(), "at "+c.target) {
				t.Fatalf("want class %q in %q", c.class, all)
			}
		})
	}
}

// Batches are bounded by bytes as well as rows. Values around the byte budget
// and the row limit: every row is encrypted exactly once, none skipped, none
// repeated, and no batch (except a lone big row) passes the budget.
func TestEncryptEnvBatchesAreBoundedByBytes(t *testing.T) {
	st, f, box := setup(t)
	sizes := []int{40, 40, 20, 100, 99, 1, 60, 41, 59, 10, 10, 10, 10}
	want := map[string]string{}
	for i, n := range sizes {
		k := fmt.Sprintf("K%02d", i)
		want[k] = strings.Repeat(string(rune('a'+i)), n)
		put(t, st, f.ServiceID, k, want[k])
	}
	var batches [][]int
	opt := encryptOptions{MaxBatchBytes: 100, MaxValueBytes: 1000, BatchSize: 5, afterRead: func(b []store.EnvVar) {
		var sz []int
		for _, v := range b {
			sz = append(sz, len(v.Value))
		}
		batches = append(batches, sz)
	}}
	res, out, err := doEncrypt(t, st, box, opt)
	if err != nil || res.Encrypted != len(sizes) || res.Scanned != len(sizes) || res.Conflicts != 0 {
		t.Fatalf("res=%+v err=%v out=%q", res, err, out)
	}
	rows := 0
	for _, b := range batches {
		sum := 0
		for _, n := range b {
			sum += n
		}
		if len(b) > 5 || (sum > 100 && len(b) > 1) {
			t.Fatalf("batch %v breaks the limits (rows %d, bytes %d)", b, len(b), sum)
		}
		rows += len(b)
	}
	if rows != len(sizes) { // the write pass reads each row once
		t.Fatalf("write pass saw %d rows in %v", rows, batches)
	}
	for k, v := range raw(t, st, f.ServiceID) {
		got, err := box.Decrypt(v)
		if err != nil || got != want[k] {
			t.Fatalf("%s: err=%v", k, err)
		}
	}
}

// With no option set, the 16 MiB default budget applies: three 6 MiB values
// do not fit one batch.
func TestEncryptEnvDefaultByteBudgetApplies(t *testing.T) {
	st, f, box := setup(t)
	for i := range 3 {
		put(t, st, f.ServiceID, fmt.Sprintf("K%d", i), strings.Repeat("v", 6<<20))
	}
	var rows []int
	res, _, err := doEncrypt(t, st, box, encryptOptions{DryRun: true, afterRead: func(b []store.EnvVar) { rows = append(rows, len(b)) }})
	if err != nil || res.Encrypted != 3 || fmt.Sprint(rows) != "[2 1]" {
		t.Fatalf("res=%+v err=%v batches=%v", res, err, rows)
	}
}

// A cancel that lands while the command reads (SIGTERM during a DB read) gives
// the same "interrupted ... run again to resume" text and a non-zero exit, not
// a bare "context canceled".
func TestEncryptEnvCancelDuringReadIsInterrupted(t *testing.T) {
	st, f, box := setup(t)
	for i := range 6 {
		put(t, st, f.ServiceID, fmt.Sprintf("K%d", i), "v")
	}
	// Pass 1: the very first read fails with context.Canceled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	_, err := encryptEnv(ctx, st, box, encryptOptions{Yes: true}, &out)
	if exitCode(err) != 1 || !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "read env vars") ||
		!strings.Contains(out.String(), "interrupted") || !strings.Contains(out.String(), "run again to resume") {
		t.Fatalf("pass 1: err=%v out=%q", err, out.String())
	}
	// Pass 2: the signal arrives after the last write of the first batch, so
	// the NEXT read fails with context.Canceled.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	out.Reset()
	calls := 0
	replace := func(c context.Context, svc, key, old, enc string) (bool, error) {
		swapped, err := st.ReplaceEnvVarIfEquals(c, svc, key, old, enc)
		if calls++; calls == 3 {
			cancel()
		}
		return swapped, err
	}
	res, err := encryptEnv(ctx, st, box, encryptOptions{Yes: true, BatchSize: 3, replace: replace}, &out)
	if exitCode(err) != 1 || strings.Contains(err.Error(), "read env vars") || res.Encrypted != 3 ||
		!strings.Contains(out.String(), "encrypted=3 so far") || !strings.Contains(out.String(), "run again to resume") {
		t.Fatalf("pass 2: res=%+v err=%v out=%q", res, err, out.String())
	}
	if res, _, err := doEncrypt(t, st, box, encryptOptions{}); err != nil || res.Encrypted+res.AlreadyEncrypted != 6 {
		t.Fatalf("resume: res=%+v err=%v", res, err)
	}
}

// "encrypted=N so far" counts a write only after it committed, and counts the
// in-flight one when it did commit although the context was cancelled.
func TestEncryptEnvInterruptedCountIsExact(t *testing.T) {
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprintf("inflight committed=%v", committed), func(t *testing.T) {
			st, f, box := setup(t)
			put(t, st, f.ServiceID, "A", "va")
			put(t, st, f.ServiceID, "B", "vb")
			put(t, st, f.ServiceID, "C", "vc")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n := 0
			replace := func(_ context.Context, svc, key, old, enc string) (bool, error) {
				n++
				if n < 2 {
					return st.ReplaceEnvVarIfEquals(context.Background(), svc, key, old, enc)
				}
				// Second write: the signal arrives while the UPDATE is in flight.
				if committed {
					if _, err := st.ReplaceEnvVarIfEquals(context.Background(), svc, key, old, enc); err != nil {
						t.Error(err)
					}
				}
				cancel()
				return false, context.Canceled
			}
			var out bytes.Buffer
			res, err := encryptEnv(ctx, st, box, encryptOptions{Yes: true, replace: replace}, &out)
			want := 1
			if committed {
				want = 2
			}
			enc := 0
			for _, v := range raw(t, st, f.ServiceID) {
				if secrets.IsEncrypted(v) {
					enc++
				}
			}
			line := fmt.Sprintf("encrypted=%d so far", want)
			if exitCode(err) != 1 || res.Encrypted != want || enc != want || !strings.Contains(out.String(), line) {
				t.Fatalf("res=%+v rows_encrypted=%d err=%v out=%q", res, enc, err, out.String())
			}
		})
	}
}

// The usage text of encrypt-env names every flag.
func TestEncryptEnvUsageShowsAllFlags(t *testing.T) {
	connect := func(context.Context, string) (*pgxpool.Pool, func(), error) {
		return nil, func() {}, errors.New("must not connect")
	}
	for _, args := range [][]string{{"--bogus"}, {"extra"}} {
		err := runEncryptEnv(context.Background(), args, goodKeyEnv(nil), connect, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

// The cap applies to the PLAINTEXT. A 7 MiB plain value is encrypted (its
// ciphertext is about 9.4 MiB, over the cap) and a later run is a clean no-op:
// the oversize enc:v1: row is already_encrypted_large, not too_large.
func TestEncryptEnvCiphertextOverCapIsNotTooLarge(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "SEVEN", strings.Repeat("s", 7<<20))
	put(t, st, f.ServiceID, "SMALL", "s")
	res, out, err := doEncrypt(t, st, box, encryptOptions{})
	if err != nil || res.Encrypted != 2 || res.TooLarge != 0 {
		t.Fatalf("first run res=%+v err=%v out=%q", res, err, out)
	}
	ct := raw(t, st, f.ServiceID)["SEVEN"]
	if !secrets.IsEncrypted(ct) || len(ct) <= defaultMaxValueBytes {
		t.Fatalf("test premise: ciphertext len %d should exceed the cap", len(ct))
	}
	for _, dry := range []bool{false, true} {
		res, out, err = doEncrypt(t, st, box, encryptOptions{DryRun: dry})
		if err != nil || exitCode(err) != 0 || res.TooLarge != 0 || res.Encrypted != 0 || res.AlreadyEncrypted != 1 || res.AlreadyEncryptedLarge != 1 || res.Scanned != 2 {
			t.Fatalf("dry=%v res=%+v err=%v out=%q", dry, res, err, out)
		}
		if !strings.Contains(out, "already_encrypted_large=1") || !strings.Contains(out, "too_large=0") {
			t.Fatalf("out=%q", out)
		}
	}
	if raw(t, st, f.ServiceID)["SEVEN"] != ct {
		t.Fatal("oversize ciphertext was rewritten")
	}
}

// A plain value over the cap stays too_large (exit 1) and is never encrypted.
func TestEncryptEnvPlain9MiBStaysTooLarge(t *testing.T) {
	st, f, box := setup(t)
	put(t, st, f.ServiceID, "NINE", strings.Repeat("n", 9<<20))
	res, _, err := doEncrypt(t, st, box, encryptOptions{})
	if exitCode(err) != 1 || res.TooLarge != 1 || res.Encrypted != 0 || res.AlreadyEncryptedLarge != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if secrets.IsEncrypted(raw(t, st, f.ServiceID)["NINE"]) {
		t.Fatal("9 MiB plain value was encrypted")
	}
}

// An oversize enc:v1: value is counted, never loaded and never decrypt-verified,
// and it counts neither as ok nor as suspect for the wrong-key guard: with a
// wrong key and plain rows present it must not make the run proceed or refuse.
func TestEncryptEnvOversizePrefixedIsUnverifiedAndNeutral(t *testing.T) {
	st, f, box := setup(t)
	// Garbage that starts with the prefix: if it were loaded it would be suspect.
	put(t, st, f.ServiceID, "LARGE", "enc:v1:"+strings.Repeat("!", 200))
	put(t, st, f.ServiceID, "PLAIN", "p")
	var loaded int
	res, out, err := doEncrypt(t, st, box, encryptOptions{MaxValueBytes: 100, afterRead: func(b []store.EnvVar) {
		for _, v := range b {
			if v.EncryptedLarge && v.Value != "" {
				loaded++
			}
		}
	}})
	if err != nil || loaded != 0 || res.Suspect != 0 || res.AlreadyEncrypted != 0 || res.AlreadyEncryptedLarge != 1 || res.Encrypted != 1 {
		t.Fatalf("res=%+v err=%v loaded=%d out=%q", res, err, loaded, out)
	}
	if !strings.Contains(out, "already_encrypted_large=1") || !strings.Contains(out, "NOT loaded or decrypt-verified") {
		t.Fatalf("out=%q", out)
	}
	// Wrong-key guard: one real ciphertext of ANOTHER key (suspect) + large prefixed
	// + plain => still refused as wrong key; the large row does not count as ok.
	st2, f2, box2 := setup(t)
	other, _ := newBox(t, 7).Encrypt("v")
	put(t, st2, f2.ServiceID, "OTHER", other)
	put(t, st2, f2.ServiceID, "LARGE", "enc:v1:"+strings.Repeat("!", 200))
	put(t, st2, f2.ServiceID, "PLAIN", "p")
	_, _, err = doEncrypt(t, st2, box2, encryptOptions{MaxValueBytes: 100})
	if !errors.Is(err, errWrongKey) {
		t.Fatalf("err=%v", err)
	}
	if raw(t, st2, f2.ServiceID)["PLAIN"] != "p" {
		t.Fatal("wrote under a wrong key")
	}
}

// A read error mid-run (the connection is killed between two batches, in pass 1
// and in pass 2) prints a fixed text and a class, never the driver text, which
// can name the DB user, the database and the host:port.
func TestEncryptEnvReadErrorsAreRedacted(t *testing.T) {
	cc := pool.Config().ConnConfig
	const user, pw = "zzreaduser", "ZZREADPW"
	// A role of its own, so the user name is a marker we can grep for.
	ctx := context.Background()
	for _, q := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' SUPERUSER`, user, pw),
	} {
		if _, err := pool.Exec(ctx, q); err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DROP ROLE IF EXISTS "+user) })
	url := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable&application_name=zzkillme", user, pw, cc.Host, cc.Port, cc.Database)
	for _, killPass := range []int{1, 2} {
		t.Run(fmt.Sprintf("pass%d", killPass), func(t *testing.T) {
			st0, f, box := setup(t)
			for i := range 6 {
				put(t, st0, f.ServiceID, fmt.Sprintf("K%d", i), "v")
			}
			p, closeP, err := connectDB(ctx, url)
			if err != nil {
				t.Fatal(err)
			}
			defer closeP()
			kill := func() {
				if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'zzkillme'`); err != nil {
					t.Fatal(err)
				}
				time.Sleep(200 * time.Millisecond)
			}
			// Pass 1: kill the idle pooled connection before the first read.
			if killPass == 1 {
				kill()
			}
			// Pass 2 (batch 3): kill right after the third write, so the next read fails.
			s := store.New(p)
			writes := 0
			replace := func(c context.Context, svc, key, old, enc string) (bool, error) {
				swapped, err := s.ReplaceEnvVarIfEquals(c, svc, key, old, enc)
				if writes++; killPass == 2 && writes == 3 {
					kill()
				}
				return swapped, err
			}
			var out bytes.Buffer
			_, err = encryptEnv(ctx, s, box, encryptOptions{Yes: true, BatchSize: 3, replace: replace}, &out)
			if err == nil {
				t.Fatal("expected a read error")
			}
			all := out.String() + err.Error()
			for _, bad := range []string{user, pw, cc.Database, fmt.Sprint(cc.Port), "failed to connect", "FATAL", "dial", "unexpected EOF", "conn closed", "terminating connection", "127.0.0.1"} {
				if strings.Contains(all, bad) {
					t.Fatalf("output contains %q: %q", bad, all)
				}
			}
			if !strings.Contains(err.Error(), "read env vars failed (") || exitCode(err) != 1 {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReadFailureClasses(t *testing.T) {
	leak := errors.New("failed to connect to `user=secretuser database=dbn`: 127.0.0.1:1 (127.0.0.1): dial error")
	for _, c := range []struct {
		err  error
		want string
	}{
		{leak, "connection error"},
		{&net.OpError{Op: "dial", Err: leak}, "network error"},
		{&pgconn.PgError{Code: "57P01", Message: "terminating connection for user secretuser"}, "SQLSTATE 57P01"},
		{context.DeadlineExceeded, "timeout"},
	} {
		got := readFailure(c.err).Error()
		if got != "read env vars failed ("+c.want+")" || strings.Contains(got, "secretuser") {
			t.Fatalf("%v -> %q", c.err, got)
		}
	}
}
