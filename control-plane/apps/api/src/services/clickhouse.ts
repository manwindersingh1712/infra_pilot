import { createClient } from "@clickhouse/client";
import { LogEntry } from "./redis.js";

const clickhouse = createClient({
  url: process.env.CLICKHOUSE_URL || "http://localhost:8123",
  username: process.env.CLICKHOUSE_USER || "default",
  password: process.env.CLICKHOUSE_PASSWORD || ""
});

const TABLE_NAME = "container_logs";

/**
 * Initialize ClickHouse table for logs
 */
export async function initClickHouse(): Promise<void> {
  await clickhouse.exec({
    query: `
      CREATE TABLE IF NOT EXISTS ${TABLE_NAME} (
        deployment_id String,
        source LowCardinality(String),
        message String,
        timestamp DateTime64(3),
        ingestion_time DateTime64(3) DEFAULT now64(3)
      ) ENGINE = MergeTree()
      ORDER BY (deployment_id, timestamp)
      PARTITION BY toYYYYMMDD(timestamp)
      TTL timestamp + INTERVAL 30 DAY
      SETTINGS index_granularity = 8192
    `
  });

  console.log("[clickhouse] Table initialized");
}

/**
 * Insert logs in batch
 */
export async function insertLogs(logs: LogEntry[]): Promise<void> {
  if (logs.length === 0) return;                                     
                                                                      
  // Build VALUES clause with proper string escaping              
  const values = logs.map(l => {                                                                                                                                                                                                                                                                              
    const ts = new Date(l.timestamp).toISOString().slice(0, -1); // Remove Z                  
    // Escape single quotes in message by doubling them                                                                                               
    const escapedMessage = l.message.replace(/'/g, "''");                                                                                             
    return `('${l.deploymentId}', '${l.source}', '${escapedMessage}', '${ts}')`;                                                                      
  }).join(', ');                                                                                                                                      
                                                                                                                                                      
  const query = `INSERT INTO ${TABLE_NAME} (deployment_id, source, message, timestamp) VALUES ${values}`;                                             

  await clickhouse.exec({ query });                                                                                                                 
}

/**
 * Query logs with filters
 */
export async function queryLogs(options: {
  deploymentId: string;
  startTime?: Date;
  endTime?: Date;
  sources?: ("stdout" | "stderr" | "system")[];
  search?: string;
  limit?: number;
  offset?: number;
}): Promise<LogEntry[]> {
  const {
    deploymentId,
    startTime,
    endTime,
    sources,
    search,
    limit = 100,
    offset = 0
  } = options;

  const conditions: string[] = [`deployment_id = '${deploymentId}'`];

  if (startTime) {
    conditions.push(`timestamp >= '${startTime.toISOString()}'`);
  }
  if (endTime) {
    conditions.push(`timestamp <= '${endTime.toISOString()}'`);
  }
  if (sources && sources.length > 0) {
    conditions.push(`source IN ('${sources.join("','")}')`);
  }
  if (search) {
    conditions.push(`message ILIKE '%${search.replace(/'/g, "''")}%'`);
  }

  const query = `
    SELECT
      deployment_id as deploymentId,
      source,
      message,
      formatDateTime(timestamp, '%Y-%m-%dT%H:%i:%s.%fZ') as timestamp
    FROM ${TABLE_NAME}
    WHERE ${conditions.join(" AND ")}
    ORDER BY timestamp DESC
    LIMIT ${limit}
    OFFSET ${offset}
  `;

  const resultSet = await clickhouse.query({ query, format: "JSONEachRow" });
  const rows = await resultSet.json<{
    deploymentId: string;
    source: LogEntry["source"];
    message: string;
    timestamp: string;
  }>();

  return rows.reverse(); // Oldest first
}

/**
 * Get log count for a deployment
 */
export async function getLogCount(deploymentId: string): Promise<number> {
  const resultSet = await clickhouse.query({
    query: `SELECT count() as count FROM ${TABLE_NAME} WHERE deployment_id = '${deploymentId}'`,
    format: "JSONEachRow"
  });

  const rows = await resultSet.json<{ count: string }>();
  return parseInt(rows[0]?.count || "0", 10);
}

export { clickhouse };
