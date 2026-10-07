# Plan 1, Task 1a: Green cp-ui lint and build baseline

## Implementation
- Engineer commit `8d4bb0c` chore(ui): fix existing lint and type errors so CI is green (9 files in cp-ui/src, +44/-55; no config, package.json or lockfile change).
- Before: lint 25 problems (20 errors, 5 warnings); `tsc -b` 10 errors (plan expected 14). After: lint 0 errors, 5 `exhaustive-deps` warnings (allowed); build passes.
- Changes: `catch (err: any)` → `catch (err)` with `instanceof Error` narrowing; typed request body in CreateServiceModal; type-only imports; dead locals removed (`runAutoLayout`, `isDeploymentPending`, unused props); four LandingPage `easing:` keys removed (ignored by motion 12.38.0, which reads `ease`).
- Deviations: 3 per-line `react-hooks/set-state-in-effect` disables with reasons (not a type rule; fixing would change render timing).

## Reviews (commit 8d4bb0c)
- **Security: CLEAN.** gitleaks clean; all error sinks are escaped JSX text; socket auth unchanged. Informational: pre-existing npm audit advisories (4 high, 1 moderate) → backlog.
- **Staff: APPROVE.** Verified motion source ignores `easing`. Lows STAFF-1 (T1a) (disables exceed CTO wording; CTO to confirm at Plan 1 gate), STAFF-2 (T1a) (= PROD-1), STAFF-3 (T1a) (unused `service` prop type) → backlog.
- **Product: APPROVE.** No user-visible change, no release note. Low PROD-1 (intended easing curves never applied; optional polish) → backlog.
- **Verifier: PASS.** Lint/build green on Node 26 and Node 20.20.2 (npm ci under Node 26). Playwright at 1440x900, new vs parent: `/`, `/login`, `/projects`, `/projects/:id` identical, no new console errors; navbar fade, animations, canvas node and details panel work; 401 login path shows "Invalid email or password". Left behind: test project `cmuy2a1mw00003u3n546iezzm` ("verify-p1t1a") and redis service row `cmuy2a7v900013u3nbqos1e6s` in the dev DB (no delete endpoint).

## Result
**Done 2026-10-07.** No open critical/high/medium. Next: Task 2.
