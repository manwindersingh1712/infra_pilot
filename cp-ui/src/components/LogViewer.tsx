import { useRef, useEffect } from "react";
import { useLogs, type LogEntry } from "../hooks/useLogs";

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
  const { logs, meta, isConnected, error, clearLogs } = useLogs({ deploymentId });
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
    <div style={{ border: "1px solid #ddd", borderRadius: 8, overflow: "hidden", height: "100%", display: "flex", flexDirection: "column" }}>
      {/* Header */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "8px 12px",
          background: "#f3f4f6",
          borderBottom: "1px solid #ddd"
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span style={{ fontWeight: 600 }}>Logs</span>
          <span
            style={{
              fontSize: 12,
              padding: "2px 8px",
              borderRadius: 4,
              background: isConnected ? "#dcfce7" : "#fee2e2",
              color: isConnected ? "#166534" : "#991b1b"
            }}
          >
            {isConnected ? "Live" : "Disconnected"}
          </span>
          {meta && (
            <span style={{ fontSize: 12, color: "#666" }}>
              Source: {meta.source}
              {meta.totalCount !== undefined && ` (${meta.totalCount} total)`}
            </span>
          )}
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <button onClick={clearLogs} style={{ fontSize: 12 }}>
            Clear
          </button>
        </div>
      </div>

      {/* Error message */}
      {error && (
        <div
          style={{
            padding: "8px 12px",
            background: "#fee2e2",
            color: "#991b1b",
            fontSize: 12
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
          background: "#1a1a1a",
          fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Consolas, monospace',
          fontSize: 13,
          lineHeight: 1.5
        }}
      >
        {logs.length === 0 ? (
          <div
            style={{
              padding: 20,
              color: "#666",
              textAlign: "center"
            }}
          >
            No logs yet. Deploy a service to see logs here.
          </div>
        ) : (
          logs.map((log, index) => (
            <div
              key={index}
              style={{
                display: "flex",
                padding: "2px 12px",
                borderBottom: "1px solid #2a2a2a"
              }}
            >
              <span
                style={{
                  color: "#6b7280",
                  minWidth: 70,
                  flexShrink: 0
                }}
              >
                {formatTimestamp(log.timestamp)}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  minWidth: 40,
                  flexShrink: 0,
                  fontWeight: 600
                }}
              >
                {sourceLabels[log.source]}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-all"
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
