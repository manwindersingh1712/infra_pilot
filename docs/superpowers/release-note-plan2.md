# Plan 2 release note

Stub. The full note is written at the Plan 2 cutover (see "Plan 2 Task 12 carry-forwards" in `docs/superpowers/review/backlog.md`).

## Teardown (deleting a service)

Deleting a service is asynchronous and permanent: the service disappears at once and its containers and URL are removed within a minute (a running build can take up to 3 minutes to stop). Teardown retries until it succeeds. If one gets stuck, see `docs/superpowers/runbook/teardown-stuck.md`.
