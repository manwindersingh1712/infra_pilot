import { useEffect, useRef } from "react";
import { animate } from "motion";

interface ToastProps {
  message: string;
  onDismiss: () => void;
  durationMs?: number;
  // "info" is a neutral notice (for example "Deleting…"); the default is an error.
  tone?: "error" | "info";
}

export function Toast({ message, onDismiss, durationMs = 6000, tone = "error" }: ToastProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (ref.current) {
      animate(ref.current, { opacity: [0, 1], transform: ["translateY(12px)", "translateY(0px)"] }, { duration: 0.2 });
    }
    const t = setTimeout(onDismiss, durationMs);
    return () => clearTimeout(t);
  }, [message, onDismiss, durationMs]);

  return (
    <div
      ref={ref}
      role={tone === "info" ? "status" : "alert"}
      style={{
        position: "fixed",
        bottom: "24px",
        left: "50%",
        marginLeft: "-180px",
        width: "360px",
        zIndex: 200,
        display: "flex",
        alignItems: "flex-start",
        gap: "12px",
        padding: "12px 16px",
        background: tone === "info" ? "#111827" : "#1f1315",
        border: tone === "info" ? "1px solid rgba(139,92,246,0.5)" : "1px solid rgba(239,68,68,0.4)",
        borderRadius: "10px",
        color: tone === "info" ? "#ddd6fe" : "#fca5a5",
        fontSize: "13px",
        lineHeight: 1.5,
        boxShadow: "0 10px 40px rgba(0,0,0,0.5)",
        boxSizing: "border-box",
      }}
    >
      <span style={{ flex: 1 }}>{message}</span>
      <button
        onClick={onDismiss}
        aria-label="Dismiss"
        style={{
          background: "transparent",
          border: "none",
          color: "#9ca3af",
          cursor: "pointer",
          fontSize: "16px",
          lineHeight: 1,
          padding: 0,
        }}
      >
        ×
      </button>
    </div>
  );
}
