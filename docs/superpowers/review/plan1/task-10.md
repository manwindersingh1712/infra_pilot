# Plan 1, Task 10: Encrypted secrets (`internal/secrets`)

## History
- 2026-10-09 (first session): the Engineer wrote `internal/secrets/{secrets.go,secrets_test.go}`, then stalled twice on the 10-minute stream watchdog before committing. The task was paused with the files untracked.

## Implementation (resumed 2026-10-09)
- A fresh Engineer confirmed both files match the plan (lines 3012-3094 and 3104-3181). The one deviation is an added doc comment on `IsEncrypted`, which golint requires.
- **Failing run:** with `secrets.go` moved aside, `go test ./internal/secrets/` gave `undefined: New` / `undefined: RandomPassword` and `FAIL infrapilot/internal/secrets [build failed]`. The file was then restored.
- **Commit** `d46e7da` feat(go): AES-256-GCM encryption for env var values.

## Reviews (commit d46e7da)
- **Staff: APPROVE.** The code matches the plan and spec §5/§10. The key-size check runs first. Each encryption gets a fresh `crypto/rand` nonce. The length guard runs before slicing. Error messages contain no plaintext, ciphertext or key. `Box` is safe for concurrent use. Lows STAFF-1..3 (T10) went to the backlog: the short-payload branch is untested, test setup errors are discarded, and the `New` doc does not say the key is base64-decoded.
- **Verifier: PASS.** Full suite green: build/vet, golangci-lint 0 issues, `go test -race -count=1 ./...` all ok, `-short` ok. The caller check (copy of the module, key from `openssl rand -base64 32`, decoded to 32 bytes) covered:
  - unicode and empty-string round trips;
  - plaintext passthrough;
  - a wrong key (1 bit off) and truncated or empty payloads, which fail with the right messages;
  - a 31-byte key, which is rejected;
  - 1000 `RandomPassword` samples, all `^[A-Za-z0-9_-]{32}$` and all unique.

  gitleaks reported no leaks.
- **Security: FINDINGS (1 medium, 2 low).** gosec clean; gitleaks clean.
  - **SEC-1 (medium, pre-existing): the toolchain had known vulnerabilities.** On go1.27.1, govulncheck exited 3 with 8 reachable stdlib vulnerabilities (GO-2026-6617/6613/6611/6610/6608/6607/6605/6603 in net/http, http2, textproto and crypto/tls), all fixed in go1.27.2.
    - **The earlier clean results were false.** The orchestrator confirmed that the wrapper `perl -e 'alarm 180; exec @ARGV' govulncheck` exits **0** when govulncheck is not on PATH, which it isn't in agent shells. So the "govulncheck exit 0" recorded for Task 9 and for the Task 10 dry run was not a real result.
    - CI was not affected: it installs govulncheck and uses `1.27.x`.
    - **Fix to the instructions:** the briefs and START-HERE now say `exec @ARGV or exit 127` and the full path `$(go env GOPATH)/bin/govulncheck`.
  - SEC-2 (fail-open plaintext passthrough, no AAD) and SEC-3 (`RandomPassword` can start with `-`; must be resolved before Task 23 puts passwords on argv) went to the backlog. So did the forward note that decrypted env must stay inside the activity.

## Fix round 1
- Engineer commit `bc63454` build(go): require go1.27.2 for stdlib security fixes (SEC-1). It changes only `go.mod` (`go 1.27.2`, no toolchain line, go.sum unchanged); GOTOOLCHAIN=auto downloads go1.27.2. golangci-lint 2.14.0 accepts the new directive.
- **Security re-check: CLEAN.** govulncheck reports "Your code is affected by 0 vulnerabilities" (one uncalled module vulnerability remains, the accepted GO-2026-5932). gosec and gitleaks are clean. CI's `1.27.x` resolves to 1.27.2 or later.
- **Verifier re-run: PASS** on go1.27.2. Build/vet, lint 0 issues, `-race` and `-short` all ok, and the caller check reports OK.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 11.
