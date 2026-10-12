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
	"strings"
	"sync/atomic"
	"testing"

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
	if !strings.Contains(out, "conflicts=1") || !strings.Contains(out, "run it again") {
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
	res, out, err := doEncrypt(t, st, box, encryptOptions{})
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
	if err := runEncryptEnv(context.Background(), nil, goodKeyEnv(nil), connect, &out); err != nil {
		t.Fatal(err)
	}
	if !secrets.IsEncrypted(raw(t, st, f.ServiceID)["A"]) || strings.Contains(out.String(), "plain-a") {
		t.Fatalf("not encrypted or leaked: %q", out.String())
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
