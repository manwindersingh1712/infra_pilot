# Plan 1, Task 17: Host port allocation

## Implementation
- Engineer commit `a3730f7` feat(go): host port allocation (`internal/runtime/{ports.go,ports_test.go}`). Plan-verbatim.

## Round 0 reviews (a3730f7)
- **Security: CLEAN.** Parser can't panic or over-allocate on hostile `docker ps` output; picker bounded by config range; nothing publishes on 0.0.0.0. Note → backlog (port floor 1024).
- **Verifier: PASS.** Real Docker: UsedPorts found 20000-20004 across IPv4/IPv6/UDP; PickFreePort skipped used + store ports (→ 20006); full range → `no_free_ports_in_range`. Noted that compact range output (`0.0.0.0:20001-20002->80-81/tcp`) would be missed.
- **Staff: CHANGES REQUESTED (0/0/1/2).** STAFF-1 medium: regex `:(\d+)->` ignores published ranges, so a repeating collision is possible. STAFF-2 low: parser test covers only two shapes. STAFF-3 low (drop of docker ps error) → backlog (Task 23). Concurrent-deploy race self-heals via Temporal activity retry (no reservation needed).

## Fix round 1
- `5bc320f` fix(go): count published port ranges as used (STAFF-1 T17, STAFF-2 T17). Regex `:(\d+)(?:-(\d+))?->`, marks every port lo..hi, skips unparsable / hi<lo / span >= 65536. Table test with 9 exact-map rows; the range row failed on the old regex, all pass now. Engineer's CI dry run green (gofmt, build/vet, lint, race, govulncheck, gosec, guards, -short).
- **Next:** Staff + Verifier re-check of `5bc320f` (Verifier: real Docker with a container publishing a range, e.g. `-p 20001-20002:80-81`), then mark this task done and start Task 18.
