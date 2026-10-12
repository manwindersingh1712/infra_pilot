import { useRef, useEffect } from "react";
import { useLogs, type LogEntry, type LogStatus } from "../hooks/useLogs";
import { emptyStateText, entryKey } from "../lib/logStream";

interface LogViewerProps {
  deploymentId: string;
}

const sourceColors: Record<LogEntry["source"], string> = {
  stdout: "#e5e7eb",
  stderr: "#fca5a5",
  system: "#93c5fd"
};

const sourceLabels: Record<LogEntry["source"], string> = {
  stdout: "OUT",
  stderr: "ERR",
  system: "SYS"
};

const badges: Record<LogStatus, { label: string; color: string; rgb: string }> = {
  live: { label: "Live", color: "#22c55e", rgb: "34,197,94" },
  reconnecting: { label: "Reconnecting…", color: "#f59e0b", rgb: "245,158,11" },
  paused: { label: "Paused", color: "#9ca3af", rgb: "156,163,175" },
  disconnected: { label: "Disconnected", color: "#ef4444", rgb: "239,68,68" },
};

const headerButton = {
  fontSize: 12,
  padding: "6px 12px",
  borderRadius: "4px",
  border: "1px solid rgba(255,255,255,0.1)",
  background: "rgba(255,255,255,0.05)",
  color: "#e5e7eb",
  cursor: "pointer",
  transition: "background 0.2s",
} as const;

const hoverOn = (e: React.MouseEvent<HTMLButtonElement>) => {
  e.currentTarget.style.background = "rgba(255,255,255,0.1)";
};
const hoverOff = (e: React.MouseEvent<HTMLButtonElement>) => {
  e.currentTarget.style.background = "rgba(255,255,255,0.05)";
};

function formatTimestamp(timestamp: string): string {
  const date = new Date(timestamp);
  return date.toLocaleTimeString("en-US", {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  });
}

export function LogViewer({ deploymentId }: LogViewerProps) {
  const { logs, meta, status, everConnected, canRetry, cleared, error, clearLogs, retry } = useLogs({ deploymentId });
  const badge = badges[status];
  const badgeLabel = status === "reconnecting" && !everConnected ? "Connecting…" : badge.label;
  const scrollRef = useRef<HTMLDivElement>(null);
  const shouldAutoScroll = useRef(true);

  // Auto-scroll to bottom
  useEffect(() => {
    if (scrollRef.current && shouldAutoScroll.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  const handleScroll = () => {
    if (scrollRef.current) {
      const { scrollTop, scrollHeight, clientHeight } = scrollRef.current;
      shouldAutoScroll.current = scrollHeight - scrollTop - clientHeight < 50;
    }
  };

  return (
    <div style={{
      border: "1px solid rgba(255,255,255,0.1)",
      borderRadius: 8,
      overflow: "hidden",
      height: "100%",
      display: "flex",
      flexDirection: "column",
      background: "#0a0a0a",
    }}>
      {/* Header */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "12px 16px",
          background: "rgba(255,255,255,0.03)",
          borderBottom: "1px solid rgba(255,255,255,0.1)",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
          <span style={{ fontWeight: 600, color: "#fff" }}>Logs</span>
          <span
            role="status"
            data-testid="log-status"
            style={{
              fontSize: 12,
              padding: "2px 8px",
              borderRadius: 4,
              background: `rgba(${badge.rgb},0.1)`,
              color: badge.color,
              border: `1px solid rgba(${badge.rgb},0.2)`,
            }}
          >
            {badgeLabel}
          </span>
          {meta && (
            <span style={{ fontSize: 12, color: "#6b7280" }}>
              Source: {meta.source}
              {meta.totalCount !== undefined && ` (${meta.totalCount} total)`}
            </span>
          )}
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          {(status === "reconnecting" || status === "disconnected") && canRetry && (
            <button onClick={retry} style={headerButton} onMouseEnter={hoverOn} onMouseLeave={hoverOff}>
              Retry
            </button>
          )}
          <button onClick={clearLogs} style={headerButton} onMouseEnter={hoverOn} onMouseLeave={hoverOff}>
            Clear
          </button>
        </div>
      </div>

      {/* Error message */}
      {error && (
        <div
          role="alert"
          style={{
            padding: "12px 16px",
            background: `rgba(${badge.rgb},0.1)`,
            color: badge.color,
            fontSize: 12,
            borderBottom: `1px solid rgba(${badge.rgb},0.2)`,
          }}
        >
          {error}
        </div>
      )}

      {/* Log content */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        style={{
          flex: 1,
          overflow: "auto",
          background: "#0a0a0a",
          fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Consolas, monospace',
          fontSize: 13,
          lineHeight: 1.5,
        }}
      >
        {logs.length === 0 ? (
          <div
            style={{
              padding: 40,
              color: "#6b7280",
              textAlign: "center",
            }}
          >
            {emptyStateText(status, cleared)}
          </div>
        ) : (
          logs.map((log) => (
            <div
              key={entryKey(log)}
              style={{
                display: "flex",
                padding: "2px 16px",
                borderBottom: "1px solid rgba(255,255,255,0.05)",
              }}
            >
              <span
                style={{
                  color: "#6b7280",
                  minWidth: 70,
                  flexShrink: 0,
                }}
              >
                {formatTimestamp(log.timestamp)}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  minWidth: 40,
                  flexShrink: 0,
                  fontWeight: 600,
                }}
              >
                {sourceLabels[log.source]}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-all",
                }}
              >
                {log.message}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
