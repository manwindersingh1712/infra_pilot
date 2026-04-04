# Container Logging Service Architecture

## Overview

The logging service provides scalable, real-time container log streaming with tiered storage for different access patterns:

- **Redis** (Hot Storage): Real-time streaming, 10-minute TTL
- **ClickHouse** (Analytics): Queryable logs, 30-day retention
- **S3/MinIO** (Cold Archive): Long-term storage, compressed NDJSON.gz

## Architecture Flow

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Docker Logs   │────▶│  Log Streamer   │────▶│  Redis Pub/Sub  │
│   (docker logs) │     │    (worker)     │     │  (real-time)    │
└─────────────────┘     └─────────────────┘     └────────┬────────┘
                                                         │
                              ┌──────────────────────────┼──────────┐
                              │                          │          │
                              ▼                          ▼          ▼
                    ┌─────────────────┐        ┌─────────────────┐  │
                    │  Redis Streams  │        │   Socket.io     │  │
                    │  (10-min TTL)   │        │   (UI clients)  │  │
                    └────────┬────────┘        └─────────────────┘  │
                             │                                      │
                             │  ┌───────────────────────────────┐   │
                             └──│   Background Flush Job (API)  │◀──┘
                                └───────────────┬───────────────┘
                                                │
                       ┌────────────────────────┼────────────────────────┐
                       │                        │                        │
                       ▼                        ▼                        ▼
            ┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
            │   ClickHouse    │      │   S3/MinIO      │      │   Redis Cleanup │
            │  (analytics)    │      │  (cold archive) │      │   (TTL expiry)  │
            └─────────────────┘      └─────────────────┘      └─────────────────┘
```

## Data Flow

### 1. Log Collection (Worker)

When a deployment succeeds, the worker starts streaming logs:

```typescript
// worker/src/runtime/log-streamer.ts
startLogStreaming(deploymentId, containerId)
  ↓
docker logs -f --tail=100 <containerId>
  ↓
For each log line:
  - Write to Redis Stream: `logs:stream:{deploymentId}`
  - Publish to Pub/Sub: `logs:pubsub:{deploymentId}`
```

**Key Points:**
- Uses `docker logs -f` to follow container output
- Redis Streams capped at 10,000 entries with MAXLEN
- 10-minute TTL on stream keys
- Separate channels for stdout/stderr/system

### 2. Real-Time Streaming (API → UI)

When user clicks a deployment in UI:

```
UI (LogViewer)
  ↓ Socket.io connect
API Socket Handler
  ↓ subscribe:logs event
Joins room `logs:{deploymentId}`
  ↓
Subscribes to Redis Pub/Sub `logs:pubsub:{deploymentId}`
  ↓
Forwards logs to Socket.io room
  ↓
UI receives `logs:new` event and appends to display
```

### 3. Background Flush (API)

Every 10 seconds (configurable), the flush job:

1. Scans for all active log streams: `logs:stream:*`
2. Reads all logs from Redis Streams
3. Inserts into ClickHouse (analytics queries)
4. Archives to S3/MinIO if >1000 logs (cold storage)

**Files:**
- `apps/api/src/jobs/flush-logs.ts` - Flush job implementation
- `apps/api/src/services/log-aggregator.ts` - Flush orchestration

## Storage Systems

### Redis (Hot Storage)

**Purpose:** Real-time access, recent logs only

**Data Structure:**
- **Streams:** `logs:stream:{deploymentId}`
  - Fields: `source`, `message`, `timestamp`
  - MAXLEN: ~10,000 entries
  - TTL: 10 minutes

**Access Patterns:**
```bash
# Read recent logs
redis-cli XRANGE logs:stream:<deployment-id> - + COUNT 100

# Check stream length
redis-cli XLEN logs:stream:<deployment-id>

# List all active streams
redis-cli KEYS "logs:stream:*"
```

**When to Use:**
- Live log tailing (most recent 10 minutes)
- Real-time alerting
- Current deployment status

### ClickHouse (Analytics)

**Purpose:** Queryable historical logs, aggregations, searches

**Schema:**
```sql
CREATE TABLE container_logs (
  deployment_id String,
  source LowCardinality(String),  -- stdout, stderr, system
  message String,
  timestamp DateTime64(3),
  ingestion_time DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
ORDER BY (deployment_id, timestamp)
PARTITION BY toYYYYMMDD(timestamp)
TTL timestamp + INTERVAL 30 DAY
```

**Access Patterns:**
```bash
# Query recent logs for a deployment
curl http://localhost:8123 -d "
  SELECT * FROM container_logs
  WHERE deployment_id = '<deployment-id>'
  ORDER BY timestamp DESC
  LIMIT 100
"

# Count logs per deployment
curl http://localhost:8123 -d "
  SELECT deployment_id, count() as cnt
  FROM container_logs
  GROUP BY deployment_id
"

# Search logs
curl http://localhost:8123 -d "
  SELECT * FROM container_logs
  WHERE message ILIKE '%error%'
  ORDER BY timestamp DESC
  LIMIT 50
"

# Logs by time range
curl http://localhost:8123 -d "
  SELECT * FROM container_logs
  WHERE deployment_id = '<deployment-id>'
    AND timestamp > now() - INTERVAL 1 HOUR
  ORDER BY timestamp DESC
"
```

**When to Use:**
- Historical log analysis
- Error searching across deployments
- Performance analytics
- Log aggregation queries

### S3/MinIO (Cold Archive)

**Purpose:** Long-term storage, compliance, backup

**Format:** NDJSON.gz (newline-delimited JSON, gzip compressed)

**Path Structure:**
```
logs/{deployment-id}/{YYYY}/{MM}/{DD}/{HH-mm-ss}_{start-ts}_{end-ts}.ndjson.gz
```

**Access Patterns:**
```bash
# List archived logs
aws --endpoint-url http://localhost:9000 s3 ls s3://cp-logs/logs/

# Download and view archived logs
aws --endpoint-url http://localhost:9000 s3 cp \
  s3://cp-logs/logs/<deployment-id>/2026/04/05/12-00-00_xxx.ndjson.gz \
  /tmp/logs.gz
gunzip -c /tmp/logs.gz | head -20

# Or use MinIO console at http://localhost:9001
# Login: minioadmin / minioadmin
```

**When to Use:**
- Compliance/audit requirements
- Long-term cost-effective storage
- Backup and disaster recovery

## How to Verify Data

### 1. Check Redis (Real-Time)

```bash
# Connect to Redis
docker exec -it cp_redis redis-cli

# List all active log streams
KEYS logs:stream:*

# Read logs from a specific deployment (replace with actual ID)
XRANGE logs:stream:<deployment-id> - + COUNT 10

# Check stream info
XINFO STREAM logs:stream:<deployment-id>

# Check Pub/Sub channels
PUBSUB CHANNELS logs:pubsub:*

# Exit
exit
```

### 2. Check ClickHouse (Analytics)

```bash
# Query total log count
curl http://localhost:8123 -d "SELECT count() FROM container_logs"

# List deployments with log counts
curl http://localhost:8123 -d "
  SELECT deployment_id, count() as log_count
  FROM container_logs
  GROUP BY deployment_id
  ORDER BY log_count DESC
"

# Check table schema
curl http://localhost:8123 -d "DESCRIBE container_logs"

# Check partitions
curl http://localhost:8123 -d "SELECT * FROM system.parts WHERE table = 'container_logs'"
```

### 3. Check MinIO/S3 (Archive)

```bash
# Using AWS CLI with MinIO endpoint
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
export AWS_DEFAULT_REGION=us-east-1

# List buckets
aws --endpoint-url http://localhost:9000 s3 ls

# List logs in bucket
aws --endpoint-url http://localhost:9000 s3 ls s3://cp-logs/logs/ --recursive

# Or open MinIO Console: http://localhost:9001
# Login: minioadmin / minioadmin
```

### 4. Check API Logs

```bash
# Watch API logs for flush job
cd control-plane && npm run dev:api

# You should see:
# [flush-logs-job] Running flush...
# [flush-logs-job] Processed: X, errors: 0
```

### 5. Check Worker Logs

```bash
# Watch worker logs for log streaming
cd control-plane && npm run dev:worker

# You should see:
# [log-streamer] starting: <deployment-id> container: <container-id>
# [log-streamer] received X lines from container
```

## Testing End-to-End

1. **Create a deployment:**
   - UI: Create project → Create service → Deploy
   - Or use API directly

2. **Check real-time logs:**
   - Click deployment row in UI
   - Should see "Live" badge
   - Logs should appear in real-time

3. **Verify Redis:**
   ```bash
   docker exec cp_redis redis-cli XLEN logs:stream:<deployment-id>
   ```

4. **Wait 10 seconds (flush interval):**
   - Check ClickHouse for logs
   - Check MinIO for archives (if >1000 logs)

5. **Refresh UI:**
   - Historical logs should load from ClickHouse
   - New logs should stream from Redis

## Configuration

### Environment Variables

**API:**
```bash
REDIS_URL=redis://localhost:6379
CLICKHOUSE_URL=http://localhost:8123
CLICKHOUSE_USER=default
CLICKHOUSE_PASSWORD=
S3_ENDPOINT=http://localhost:9000
S3_BUCKET=cp-logs
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
```

**Worker:**
```bash
REDIS_URL=redis://localhost:6379
```

### Flush Interval

Default: 10 seconds (for testing)
Production: 5 minutes

Edit in `apps/api/src/jobs/flush-logs.ts`:
```typescript
const FLUSH_INTERVAL_MS = 10 * 1000; // 10 seconds
```