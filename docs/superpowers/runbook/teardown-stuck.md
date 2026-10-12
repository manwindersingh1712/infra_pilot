# Runbook: a service delete (teardown) is stuck or failed

Deleting a service hides it at once and queues a `teardown.requested` outbox event. The worker starts the Temporal workflow `teardown-<serviceId>`, which cancels the service's in-flight deploys, removes its containers and nginx route, removes the data volume only if the delete asked for it (`purgeData=true`), and scrubs its env vars, connections and repo URL.

Every teardown activity retries without limit (10 s, doubling, capped at 5 min between attempts). Only `InvalidInput` (a service id that is not a cuid) and `NotDeleted` (the service is not soft-deleted) are permanent. So a teardown that cannot finish is a Running workflow with a failing activity, not a silent failure.

`:sid` below is the service id. In `psql` set it first: `\set sid 'cm...'`. In the shell set it ONCE and check it before any other command (copy the id from the step 0 row; never retype it per command):

```bash
SID=cm...            # the id shown by step 0
[[ "$SID" =~ ^[a-z0-9]{20,32}$ ]] || { echo bad id; exit 1; }
```

Every shell command below uses `"$SID"`. Never drop the `=$SID` part of a filter: `label=cp.serviceId` without a value matches every service container.

## 0. Check the service first (mandatory)

Before any other step that names a service, run this and read the row:

```sql inspect-service
SELECT "id", "name", "serviceType", "deletedAt" FROM "Service" WHERE "id" = :'sid';
```

`deletedAt` must be a timestamp and the name and type must be the service you mean. No row, or a NULL `deletedAt`, means the id is wrong or the service is live: stop. Every destructive or restart statement below also carries a soft-delete guard, so a mistyped id for a live service changes nothing (`0` rows affected), but do not rely on that alone.

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

```sql dead-outbox
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
  AND EXISTS (SELECT 1 FROM "Service" WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL)
ORDER BY "createdAt" DESC
LIMIT 1;
```

`INSERT 0 1` means a row was queued. `INSERT 0 0` means the service never had a teardown event (it was not deleted through the API), or it is not soft-deleted (wrong id: go back to step 0). If a Running workflow is already there, the new event is a no-op (the starter treats "already started" as success).

## 4. Last resort: clean up by hand

Only when the cause cannot be fixed (for example the service's containers cannot be removed by the worker). The service must already be soft-deleted.

List what matches first, read it, and only then remove:

```bash
docker ps -a --filter "label=cp.serviceId=$SID"          # read this list; it must be only this service
IDS=$(docker ps -aq --filter "label=cp.serviceId=$SID")
[ -n "$IDS" ] && docker rm -f $IDS                        # never run rm with an empty filter or list
ls control-plane/infra/nginx/conf.d/routes/"svc-$SID.conf"
rm control-plane/infra/nginx/conf.d/routes/"svc-$SID.conf"
docker exec cp_nginx nginx -s reload
```

Then scrub the rows (env vars, connections, repo URL) with the restart in step 3, or by hand. A managed database keeps its generated password row here (section 5 removes it when you purge the volume); the statements change nothing for a service that is not soft-deleted:

```sql scrub-rows
DELETE FROM "EnvVar" e WHERE e."serviceId" = :'sid'
  AND EXISTS (SELECT 1 FROM "Service" s WHERE s."id" = e."serviceId" AND s."deletedAt" IS NOT NULL
    AND NOT (s."serviceType" IN ('mongodb', 'redis') AND e."key" IN ('REDIS_PASSWORD', 'MONGO_INITDB_ROOT_PASSWORD')));
DELETE FROM "ServiceConnection" WHERE ("sourceId" = :'sid' OR "targetId" = :'sid')
  AND EXISTS (SELECT 1 FROM "Service" WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL);
UPDATE "Service" SET "repoUrl" = NULL WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL;
```

## 5. Orphan volumes (kept data)

A managed database (MongoDB, Redis) deleted with `purgeData=false` keeps its volume `cp-vol-<serviceId>` on the server and its generated password row (encrypted). The data cannot be re-attached from Infra Pilot; an operator can recover it. A kept volume cannot be purged later by an event, but this holds only while the completed `teardown-<serviceId>` workflow is still inside the Temporal namespace retention window (it holds the workflow id); after that a late event could start a second teardown, so purge a kept volume by hand as below. To purge it:

1. List the candidates. Choose volumes only from this SQL list (soft-deleted managed databases that still have their password row), never by hand from `docker volume ls`:

```sql orphan-volumes
SELECT s."id", 'cp-vol-' || s."id" AS volume, s."name", s."deletedAt" FROM "Service" s
WHERE s."deletedAt" IS NOT NULL AND s."serviceType" IN ('mongodb', 'redis')
  AND EXISTS (SELECT 1 FROM "EnvVar" e WHERE e."serviceId" = s."id" AND e."key" IN ('REDIS_PASSWORD', 'MONGO_INITDB_ROOT_PASSWORD'))
ORDER BY s."deletedAt";
```

2. For one id from that list, run step 0, then set and check the id and remove the volume:

```bash
SID=cm...            # one id from the list above
[[ "$SID" =~ ^[a-z0-9]{20,32}$ ]] || { echo bad id; exit 1; }
docker volume ls --filter "name=cp-vol-$SID"              # exactly one line expected
docker volume rm "cp-vol-$SID"                            # no -f: an in-use volume is refused
```

3. Delete the kept password row. It touches only the two reserved password keys of a soft-deleted managed service:

```sql purge-password
DELETE FROM "EnvVar" WHERE "serviceId" = :'sid' AND "key" IN ('REDIS_PASSWORD', 'MONGO_INITDB_ROOT_PASSWORD')
  AND EXISTS (SELECT 1 FROM "Service" WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL AND "serviceType" IN ('mongodb', 'redis'));
```

Never remove a volume of a live service.

## 6. Alert

```
increase(cp_teardown_activity_failures_total[30m]) > 0   for 30m
```

fires when some teardown activity has been failing for 30 minutes: a stuck teardown. Follow steps 1 and 2. Also alert on dead teardown events (the SQL in step 1).
