interface CanvasToolbarProps {
  onAutoLayout: () => void;
  onRefresh: () => void;
  loading: boolean;
  serviceCount: number;
  connectionCount: number;
}

export function CanvasToolbar({
  onAutoLayout,
  onRefresh,
  loading,
  serviceCount,
  connectionCount
}: CanvasToolbarProps) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        padding: "12px 16px",
        background: "rgba(255,255,255,0.02)",
        borderBottom: "1px solid rgba(255,255,255,0.1)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
        <h3 style={{ margin: 0, fontSize: "16px", fontWeight: 600, color: "#fff" }}>
          Infrastructure Canvas
        </h3>
        <div style={{ fontSize: "13px", color: "#9ca3af" }}>
          {serviceCount} service{serviceCount !== 1 ? "s" : ""}
          {connectionCount > 0 && ` • ${connectionCount} connection${connectionCount !== 1 ? "s" : ""}`}
        </div>
      </div>

      <div style={{ display: "flex", gap: "8px" }}>
        <button
          onClick={onAutoLayout}
          disabled={loading}
          style={{
            padding: "6px 12px",
            fontSize: "13px",
            borderRadius: "6px",
            border: "1px solid rgba(255,255,255,0.1)",
            background: "rgba(255,255,255,0.05)",
            color: "#e5e7eb",
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.6 : 1,
            transition: "background 0.2s",
          }}
          onMouseEnter={(e) => {
            if (!loading) {
              e.currentTarget.style.background = "rgba(255,255,255,0.1)";
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.background = "rgba(255,255,255,0.05)";
          }}
        >
          Auto Layout
        </button>
        <button
          onClick={onRefresh}
          disabled={loading}
          style={{
            padding: "6px 12px",
            fontSize: "13px",
            borderRadius: "6px",
            border: "1px solid rgba(255,255,255,0.1)",
            background: "rgba(255,255,255,0.05)",
            color: "#e5e7eb",
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.6 : 1,
            transition: "background 0.2s",
          }}
          onMouseEnter={(e) => {
            if (!loading) {
              e.currentTarget.style.background = "rgba(255,255,255,0.1)";
            }
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.background = "rgba(255,255,255,0.05)";
          }}
        >
          Refresh
        </button>
      </div>
    </div>
  );
}
