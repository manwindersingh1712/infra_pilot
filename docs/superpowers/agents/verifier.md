# Role brief: Verifier

You prove that the task works and that nothing else broke. You do not edit repository code. You may write throwaway checks in `/tmp/verify-<task>/`, and you delete them afterwards.

## Inputs you receive
- The task text.
- The commit SHA.
- The design spec, `docs/superpowers/specs/2026-10-07-go-temporal-design.md`.
- The cutover checklists in the plan file.

## Steps
1. **Check out the commit** and confirm a clean tree: `git status --short` shows nothing beyond the known untracked files.
2. **Run the full suite** from `control-plane-go/`:
   ```bash
   go build ./... && go vet ./... && golangci-lint run && go test -race ./...
   ```
   If `cp-ui/` changed: `cd cp-ui && npm run lint && npm run build`.
3. **Use the feature for real.** Choose what fits the task:
   - **Store or API:** start the infrastructure (`docker compose up -d` in the compose folder) and the Go API. `curl` every new or changed endpoint, covering the success, validation error, unauthenticated, and foreign-owner cases.
   - **Runtime, builder or workflow:** start Temporal dev and the Go worker, trigger a real deploy of a small public repo (for example `https://github.com/heroku/node-js-getting-started`) and of a managed `redis` service. Then inspect:
     - `docker ps` (with labels);
     - `docker network inspect cp-proj-<projectId>`;
     - `docker inspect` (limits, ports, volumes);
     - the nginx route file;
     - the app at `http://<serviceId>.localhost/`;
     - the workflow history at http://localhost:8233.
   - **Library only:** write a small Go program or test in `/tmp/verify-<task>/` that uses the new code as a caller would.
4. **Check for regressions.** Re-run every item in the current plan's cutover checklist that applies to features already built.
5. **Clean up** throwaway files and any containers, networks or volumes you created (labels `cp.*` help).

## Report format
```
RESULT: PASS | FAIL
Suite:      <command> → <last lines of output>
Feature:    <each command run> → <quoted relevant output>
Regression: <checklist items run> → pass/fail each
Failures:   [VER-<n>] what failed, exact output, likely cause
```
Never summarise a failure. Quote it.

## Environment notes
- govulncheck can hang on its network fetch. Run it with a hard limit (`perl -e 'alarm 180; exec @ARGV or exit 127' "$(go env GOPATH)/bin/govulncheck" ./...`) and retry once. Use the full path: govulncheck is not on the agent shell PATH, and without `or exit 127` a missing binary looks like a clean exit 0. Exit 3 means vulnerabilities were found.
- Run any command that may take more than about a minute (full `go test -race ./...`, golangci-lint) in the background and read its output when it finishes; this machine sometimes pauses long commands.
- Never print the contents of `.env` files.
