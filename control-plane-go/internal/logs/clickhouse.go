package logs

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS container_logs (
  deployment_id String,
  source LowCardinality(String),
  message String,
  timestamp DateTime64(3),
  ingestion_time DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
ORDER BY (deployment_id, timestamp)
PARTITION BY toYYYYMMDD(timestamp)
TTL toDateTime(timestamp) + INTERVAL 30 DAY
SETTINGS index_granularity = 8192`

// Query filters log lookups. Start/End are inclusive; Limit defaults to 100.
type Query struct {
	DeploymentID string
	Start, End   *time.Time
	Sources      []string
	Search       string
	Limit        int
	Offset       int
}

type ClickHouse struct{ conn driver.Conn }

// NewClickHouse connects over HTTP, e.g. rawURL "http://localhost:8123".
func NewClickHouse(rawURL, user, password string) (*ClickHouse, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse CLICKHOUSE_URL: %w", err)
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Protocol: clickhouse.HTTP,
		Addr:     []string{u.Host},
		Auth:     clickhouse.Auth{Database: "default", Username: user, Password: password},
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	return &ClickHouse{conn: conn}, nil
}

func (c *ClickHouse) Close() error { return c.conn.Close() }

// Init creates the logs table if it does not exist.
func (c *ClickHouse) Init(ctx context.Context) error { return c.conn.Exec(ctx, createTableSQL) }

func (c *ClickHouse) Insert(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO container_logs (deployment_id, source, message, timestamp)")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := batch.Append(e.DeploymentID, e.Source, e.Message, ParseTime(e.Timestamp)); err != nil {
			return err
		}
	}
	return batch.Send()
}

// Query returns matching entries oldest-first (the newest `Limit` of them).
func (c *ClickHouse) Query(ctx context.Context, q Query) ([]Entry, error) {
	sql, args := buildQuery(q)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts time.Time
		if err := rows.Scan(&e.DeploymentID, &e.Source, &e.Message, &ts); err != nil {
			return nil, err
		}
		e.Timestamp = FormatTime(ts)
		out = append(out, e)
	}
	slices.Reverse(out)
	return out, rows.Err()
}

func (c *ClickHouse) Count(ctx context.Context, deploymentID string) (int, error) {
	var n uint64
	err := c.conn.QueryRow(ctx, "SELECT count() FROM container_logs WHERE deployment_id = ?", deploymentID).Scan(&n)
	return int(n), err
}

func buildQuery(q Query) (string, []any) {
	where := []string{"deployment_id = ?"}
	args := []any{q.DeploymentID}
	if q.Start != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, *q.Start)
	}
	if q.End != nil {
		where = append(where, "timestamp <= ?")
		args = append(args, *q.End)
	}
	if len(q.Sources) > 0 {
		where = append(where, "has(?, source)")
		args = append(args, q.Sources)
	}
	if q.Search != "" {
		where = append(where, "message ILIKE ?")
		args = append(args, "%"+q.Search+"%")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	sql := "SELECT deployment_id, source, message, timestamp FROM container_logs" +
		" WHERE " + strings.Join(where, " AND ") +
		" ORDER BY timestamp DESC LIMIT " + strconv.Itoa(limit) + " OFFSET " + strconv.Itoa(q.Offset)
	return sql, args
}
