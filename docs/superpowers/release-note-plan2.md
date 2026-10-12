# Plan 2 release note

Stub. The full note is written at the Plan 2 cutover (see "Plan 2 Task 12 carry-forwards" in `docs/superpowers/review/backlog.md`).

## Teardown (deleting a service)

Deleting a service is asynchronous and permanent: the service disappears at once and its containers and URL are removed within a minute (a running build can take up to 3 minutes to stop). Teardown retries until it succeeds. If one gets stuck, see `docs/superpowers/runbook/teardown-stuck.md`.

### Managed databases (MongoDB, Redis) deleted with `purgeData=false`

The data volume `cp-vol-<serviceId>` is kept on the server, together with ONE encrypted generated-password row, so an operator can recover the data. It cannot be re-attached from the UI. Creating the service again gives a new service id and an empty volume. To remove a kept volume, see section 5 of the runbook.
