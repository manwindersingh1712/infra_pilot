# Task 7 close-out (orchestrator)

Reviews of f14c798: Security (0 critical/high/medium, 2 low, 1 info), Staff (approve, 3 low; 7 of 8 mutations caught), Verifier (pass; `go test -race -count=1 -p 2 ./...` all 18 packages ok; end-to-end on real Postgres), Product (approve; UI items to Task 15). Records: task-7-{security,staff,verifier,product}.md.

Fix round 1 (small, low findings only): 71ecc1b (SEC-1 `pathID` validator), f222492 (SEC-2 `ErrNoSuchUser` -> 401), 54b1003 (STAFF-1 body fields ignored, rune bounds; mutation-proven), 2ac9914 (PROD-3 odd stored names still load). Engineer ran build, vet, golangci-lint (0 issues), gosec (clean), gitleaks from the repo root (344 commits, no leaks), `go test -race -count=1 -p 2 ./internal/api/` and the full suite (all ok). The orchestrator re-read `pathID` and ran a read-only count against the dev DB: 0 existing ids in Project/Service/Deployment/User/EnvVar/ServiceConnection fail `^[A-Za-z0-9_-]{1,64}$`. No fresh reviewer round was run (low-severity fix round; judgement recorded here).

Rule for Tasks 8-12: every handler that reads `{id}` from the path must use `pathID(r)` and answer an invalid id exactly like a missing one. Remaining lows (SEC-3/STAFF-2 name format characters, Task 15 UI items) are in backlog.md.
