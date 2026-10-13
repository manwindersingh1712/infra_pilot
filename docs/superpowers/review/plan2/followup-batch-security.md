# Plan 2 follow-up batch: security review

Range: 7c29d61..HEAD (f68b8ab). Reviewer: Security. No code changed.

## Tool results
- golangci-lint run: 0 issues.
- gosec -quiet -exclude=G204 ./...: no findings.
- govulncheck ./...: no vulnerabilities affecting the code (1 in a required module that is not called; no impact).
- gitleaks detect (full history, 522 commits): no leaks.
- cd cp-ui && npm audit --omit=dev: 0 vulnerabilities.

## Findings

[SEC-1] severity: low
where: control-plane-go/internal/api/auth.go:223 (loopbackHost)
problem: net.SplitHostPort does not validate the port, so a Host of `127.0.0.1:80@evil` or `localhost:x` is accepted. A browser cannot send such a Host (a rebinding attack always carries the attacker's own name, which is rejected), so this is not exploitable for DNS rebinding. Trailing dot (`localhost.`), IPv6 zone (`[::1%lo0]`), userinfo without port, and subdomains (`a.localhost`) all fail closed (403). Upper case is accepted via EqualFold. Absolute-form targets set r.Host from the URL in Go's server, and X-Forwarded-Host is never read. Behind a dev reverse proxy that rewrites Host, the endpoint would 403 (fail closed).
fix: optional hardening: require the port part to be all digits. No block.

[SEC-2] severity: low
where: control-plane-go/internal/api/services.go:253
problem: the reveal audit line is identical for a full reveal and a filtered one, and also logs when keys= is empty (nothing revealed). It correctly omits key names and values, but a reviewer cannot tell a narrow reveal from reveal-all.
fix: add `"filtered", only != nil` and `"count", n` (a number, not names) to the audit line.

[SEC-3] severity: info
where: control-plane-go/internal/api/projects.go:33, :46 (validProjectName/cleanName)
problem: strings.TrimSpace removes Unicode White_Space only. Zero-width and bidi format characters (U+200B, U+FEFF, U+202E) are Cf, not control, so they pass and can pad or visually spoof names. There is no DB uniqueness constraint on names, so trimming creates no collision or uniqueness issue. Names are never used in nginx, container names, shell, or logs-as-commands (IDs are). The UI renders through React, so no injection. Pre-existing behaviour, not a regression.
fix: optional: reject unicode.Is(unicode.Cf, r) in validProjectName.

## Checked and found sound

(a) Rate limiter (ratelimit.go:97-110). take runs under l.mu, so reserve/cancel is atomic per key. A denied request is cancelled at the same `now` it was reserved, so tokens are fully restored (lastEvent == timeToAct under the lock): a flood of denied requests cannot starve a victim sharing the key, nor push the refill back, and cannot gain extra tokens (cancel only restores what it took). !r.OK() (n > burst) is unreachable since burst >= 1 and returns 1 minute. Delay is always > 0 on the cancel path; Retry-After = max(1, ceil(secs)), bounded by every*1 (strictEvery/refreshEvery constants), so no negative or huge value. 
(b) Reveal keys: filter is exact-match against stored keys, so it can only narrow, never widen; reveal=true without keys is unchanged (only == nil). keys= present but empty reveals nothing (non-nil empty map). 51+ distinct-or-not entries returns 400 (cap counts raw non-blank entries, so duplicates also count; fine). Ownership is checked first (ownedService, 404 for foreign). Key names are not echoed in errors or logs. Only the first keys parameter is read. Decrypt happens only for listed keys, which is the intended reduction.
(d) XFF (ratelimit.go:160-179): ip:port, [v6]:port, [v6] all reduce to the bare address, so ports cannot create bucket multiplicity (same key regardless of port). IPv4-mapped is unmapped, zones stripped, IPv6 keyed by /64 via clientKey, so v4 and mapped forms share one bucket and v6 cannot rotate within a /64. Any unparsable entry falls back to the peer (fail safe); the header is only honoured from trusted peers and the walk is right-to-left, so left-side spoofed entries are ignored. A trusted proxy entry written with a port is now correctly recognised as trusted.
(e) config.go: production without an explicit CORS_ORIGINS fails in RequireAPI only (worker/migrate unaffected). WarnIfLegacyEnv logs only the source name and the mode, no secrets; LogValue adds appEnvSource only. A literal `*` in CORS_ORIGINS matches no real Origin (exact slice match), so it cannot open CORS; Allow-Credentials is never set.
(g) Route walk: the only API mux is created in server.go:93 and every pattern goes through routes(router), so none is registered outside it. The metrics muxes (internal/metrics/metrics.go:147) are separate listeners bound to loopback by default, not the API. Public list is exactly healthz, readyz, auth x4, dev/login, and the ticketed log stream, and the test requires the listed ones to exist and answer non-401 while all others must answer 401 "unauthorized" unauthenticated. The test fails on an unfilled wildcard, so a new `{x}` route cannot slip through silently.
(h) .gitleaksignore (c450514): the literal in 3c83907 was base64 of "run-test-encryption-key-32-bytes", an obvious throwaway; HEAD builds it at run time. The entry is scoped to one fingerprint (commit:file:rule:line), not a path or rule wildcard. Full-history gitleaks is clean.
(i) UI: header Delete button opens the same confirm dialog (no direct delete), so the guard is unchanged. The Stopped badge is cosmetic. repoUrlProblem is client-only; the server validates (https only), so bypass is not a concern. revealKeysPath URL-encodes the service id and each key, and the comma is the only separator, so no parameter injection; the Connection tab now requests only its password key (reduction in decrypted secrets). Silent-reconnect cap (3 per 5 minutes) and the connected/openedAt reset are correct and bound the reconnect loop.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 2 low, 1 info)

APPROVE
