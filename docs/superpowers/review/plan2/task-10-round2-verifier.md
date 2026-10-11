# Plan 2 Task 10, fix round 1: Verifier re-check

RESULT: PASS (HEAD 5f51cbd; commits e2d71ed 68b30b5 6ebc650 6bf0d29 5f51cbd on 757c55e)

## Suite (control-plane-go/)
- go build ./... -> ok; go vet ./... -> ok
- golangci-lint run --allow-parallel-runners -> `0 issues.`
- gosec -quiet -exclude=G204 ./... -> no findings (empty output)
- gitleaks git --redact (repo root) -> `371 commits scanned ... no leaks found`
- go test -race -count=3 -p 2 ./internal/api/ ./internal/store/ -> `ok infrapilot/internal/api 215.352s`, `ok infrapilot/internal/store 15.337s`
- go test -race -count=1 -p 2 ./... -> every package ok except internal/worker/outbox: `wait for reaper ...: context deadline exceeded` (testcontainers Ryuk start timeout under load, not a code failure). Rerun alone: `go test -race -count=1 ./internal/worker/outbox` -> `ok ... 5.624s`.

## Real end-to-end (git archive HEAD copy outside repo, Postgres 16 via testcontainers, real TCP httptest server, users via store; tests deleted afterwards)
Seeded: A (projects pA with a1,a2,aDel[soft-deleted]; pA2 with a3), B (pB with b1,b2). Legacy SQL rows: a1->b1 and a2->b2 (label SECRET-LABEL-FROM-A), b1->a1, b2->a2, a1->a3 and a3->a2 (same user, two projects), a1->aDel and aDel->a2 (soft-deleted), plus normal a1->a2 and b1->b2.
- Canvas A (200): `{"connections":[{"id":"cmv4dw1hj000lf33nphqkgcl6","sourceId":"...a1","targetId":"...a2","label":"normal-A"}],"services":[{"id":..,"name":"a1","serviceType":"docker","positionX":0,"positionY":0,"deploymentCount":0},{..."a2"...}]}`
- Canvas B (200): one connection, label `normal-B`, services b1,b2 only.
- Byte grep of both raw bodies for the foreign service ids, a3, aDel, SECRET-LABEL-FROM-A, B-LABEL, SAMEUSER, DELETED, SECRETPASS and `repoUrl`: zero matches. Exactly 1 connection each. No `repoUrl` key in canvas services.
- Create/delete: chain x->y 200, y->z 200; duplicate 409 `Connection already exists`; self 400 `Cannot connect service to itself`; foreign, other-project, soft-deleted, missing, malformed target and foreign source all 404 with the identical body `{"error":"Service not found"}`; delete by B 404 `Connection not found`; delete by A 200; delete again 404.
- Bulk PUT: 500 entries 200; 501 entries 400 `invalid_request`; bulk with a valid entry followed by an out-of-range one -> 400 and the valid entry's position unchanged (0,0, not 111,222). Note this bad entry is caught by pre-transaction validation; mid-transaction rollback is covered by the repo's own atomicity test (6ebc650), which I did not re-derive here.
- Auto-layout chain: x = 0, 380, 760 (y 0). Diamond: s (0,0), l (380,0), r (380,230), e (760,0).
- Perf 300 services / 600 edges: canvas read 14-16 ms (111 KB), auto-layout 75-77 ms, 3 runs each.
- Services create/list through the API: repoUrl `https://****@github.com/o/r.git`, the password never appears.

## Dev environment (read-only)
- `go run ./cmd/migrate status`: 00001-00003 applied, `00004 00004_refresh_family.sql pending` (not applied).
- ~/cp-logs/go-worker.log: only `flushed logs processed=0 errors=0` info lines; no level=ERROR / WARN lines.
- Node API: `curl localhost:8080/healthz` -> `{"ok":true} HTTP200`.

## Notes
- First e2e attempt hung because of an infinite loop in my own throwaway test (edge generator); killed, fixed, rerun. A leftover postgres container from that kill was removed. Throwaway archive deleted.
- Untracked `task-10-round2-security.md` in the review dir belongs to another reviewer, not touched.
