# Runbook: a service delete (teardown) is stuck or failed

Deleting a service hides it at once and queues a `teardown.requested` outbox event. The worker starts the Temporal workflow `teardown-<serviceId>`, which cancels the service's in-flight deploys, removes its containers and nginx route, removes the data volume only if the delete asked for it (`purgeData=true`), and scrubs its env vars, connections and repo URL.

Every teardown activity retries without limit (10 s, doubling, capped at 5 min between attempts). Only `InvalidInput` (a service id that is not a cuid) and `NotDeleted` (the service is not soft-deleted) are permanent. So a teardown that cannot finish is a Running workflow with a failing activity, not a silent failure.

`:sid` below is the service id. In `psql` set it first: `\set sid 'cm...'`.

## 1. Find stuck teardowns

Temporal UI (http://localhost:8233), Workflows, advanced query:

```
WorkflowId STARTS_WITH "teardown-" AND ExecutionStatus = "Running"
```

Open the workflow. A stuck one shows a pending activity (`CancelServiceDeploys` or `TeardownService`) with attempt 5 or higher and the last failure message. Also look for finished ones that did not succeed:

```
WorkflowId STARTS_WITH "teardown-" AND ExecutionStatus IN ("Failed", "Terminated", "Canceled", "TimedOut")
```

Permanently rejected events never reach Temporal. List them:

```sql
SELECT "id", "payload"->>'serviceId' AS service, "attempts", "lastError", "createdAt"
FROM "OutboxEvent"
WHERE "type" = 'teardown.requested' AND "status" = 'dead'
ORDER BY "createdAt" DESC;
```

The worker log line `teardown event rejected as permanent` carries the `serviceId`.

## 2. Fix the cause

Read the failure in the pending activity. Typical causes: the Docker daemon is down or hung, the nginx container (`cp_nginx`) is missing so the reload fails, Postgres is unreachable, the legacy volume root cannot be read. Fix it. A Running workflow finishes on its own within 5 minutes (the longest retry interval). You do not need to do anything else.

The alert counter is `cp_teardown_activity_failures_total{step="cancel|teardown"}` on the worker metrics endpoint (`127.0.0.1:9091/metrics`).

## 3. Restart a Failed or Terminated teardown, or a dead outbox row

A completed teardown cannot be restarted (its workflow id is taken; this is intended). A Failed, Terminated, Canceled or TimedOut one is restarted by a new outbox event. Insert a copy of the latest teardown event of the service. Its payload is copied as it is, so `purgeData` is preserved. The outbox starter picks it up within a second:

```sql restart-teardown
INSERT INTO "OutboxEvent" ("id", "type", "payload", "status", "attempts")
SELECT 'rt' || md5(random()::text || clock_timestamp()::text), "type", "payload", 'pending', 0
FROM "OutboxEvent"
WHERE "type" = 'teardown.requested' AND "payload"->>'serviceId' = :'sid'
ORDER BY "createdAt" DESC
LIMIT 1;
```

`INSERT 0 1` means a row was queued. `INSERT 0 0` means the service never had a teardown event (it was not deleted through the API). If a Running workflow is already there, the new event is a no-op (the starter treats "already started" as success).

## 4. Last resort: clean up by hand

Only when the cause cannot be fixed (for example the service's containers cannot be removed by the worker). The service must already be soft-deleted.

```
docker rm -f $(docker ps -aq --filter label=cp.serviceId=<id>)
rm control-plane/infra/nginx/conf.d/routes/svc-<id>.conf
docker exec cp_nginx nginx -s reload
```

Then scrub the rows (env vars, connections, repo URL) with the restart in step 3, or by hand:

```sql
DELETE FROM "EnvVar" WHERE "serviceId" = :'sid';
DELETE FROM "ServiceConnection" WHERE "sourceId" = :'sid' OR "targetId" = :'sid';
UPDATE "Service" SET "repoUrl" = NULL WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL;
```

## 5. Orphan volumes (kept data)

A managed database (MongoDB, Redis) deleted with `purgeData=false` keeps its volume `cp-vol-<serviceId>` on the server and its generated password row (encrypted). The data cannot be re-attached from Infra Pilot; an operator can recover it. A kept volume cannot be purged later by an event (the workflow id is taken by the completed run). To purge it:

```
docker volume ls --filter name=cp-vol-          # compare with live service ids
docker volume rm cp-vol-<serviceId>             # only for a service that is deleted
```

```sql
DELETE FROM "EnvVar" WHERE "serviceId" = :'sid';   -- the kept password row
```

Check first that the id is a deleted service: `SELECT "deletedAt" FROM "Service" WHERE "id" = :'sid';` must return a timestamp. Never remove a volume of a live service.

## 6. Alert

```
increase(cp_teardown_activity_failures_total[30m]) > 0   for 30m
```

fires when some teardown activity has been failing for 30 minutes: a stuck teardown. Follow steps 1 and 2. Also alert on dead teardown events (the SQL in step 1).
