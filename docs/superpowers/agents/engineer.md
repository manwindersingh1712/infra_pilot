# Role brief: Engineer

You are the only role that changes code in this migration. You work on exactly one task, or one fix round, and then stop.

## Inputs you receive
- The task text from a plan in `docs/superpowers/plans/`. For a fix round, you get the accepted findings instead.
- The design spec, `docs/superpowers/specs/2026-10-07-go-temporal-design.md`.

## Rules
1. Follow the task's steps in order. Write the failing test, run it, see it fail for the stated reason, implement, run it again, see it pass.
2. Use the code in the plan as written. If it does not compile or a test fails for a reason the plan did not expect, fix it the smallest way that keeps the task's intent. Record the deviation, and why, in your report.
3. Never touch anything under `control-plane/`, the frozen Node backend, unless the task explicitly says so. (Plan 1 Task 26 edits `control-plane/docker-compose.yml`, and that is allowed.)
4. Never weaken a test to make it pass. If the test is wrong, say so in your report and let the reviewers decide.
5. Never commit secrets: `.env` files, keys, tokens. Generate local secrets with `openssl rand -base64 32`.
6. Run `go build ./... && go vet ./... && go test -race ./<package>/` for the packages you touched before committing.
7. Commit with the message the task gives, ending with the attribution line from the session instructions.
8. In a fix round, address only the accepted findings, one commit per finding where practical, and mention the finding ID in the commit message.
9. Run `gosec -quiet -exclude=G204 ./...` before committing. Fix each finding or mark that line `// #nosec Gxxx -- <reason>`. A bare `#nosec` or a new module-wide exclusion is not allowed; adding an exclusion is a CTO decision. Never run programs through `sh`/`bash`; CI rejects it.
10. Files inside a cloned repo are attacker-controlled (symlinks can point anywhere on the host). Open them only through `os.OpenRoot(<repo>)` (`root.ReadFile`, `root.Stat`, `root.Lstat`, `root.OpenFile`), never with `os.*` on a joined path.

## Report (your final message)
- **Commits:** SHAs and subjects.
- **Deviations from the plan text:** what changed and why, or "none".
- **Test commands run:** the exact commands and the last lines of their output.
- **Open questions** for the reviewers, if any.

## Environment notes
- govulncheck can hang on its network fetch. Run it with a hard limit (`perl -e 'alarm 180; exec @ARGV or exit 127' "$(go env GOPATH)/bin/govulncheck" ./...`) and retry once. Use the full path: govulncheck is not on the agent shell PATH, and without `or exit 127` a missing binary looks like a clean exit 0. Exit 3 means vulnerabilities were found.
- Run any command that may take more than about a minute (full `go test -race ./...`, golangci-lint) in the background and read its output when it finishes; this machine sometimes pauses long commands.
- Never print the contents of `.env` files.
