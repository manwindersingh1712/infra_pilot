import { useEffect, useRef, useState } from "react";
import { apiFetch } from "../lib/api";
import {
  beginDelete, cancelDelete, classifyDeleteError, deletePath, failDelete, isManagedDb, openDelete,
  type DeleteChoice, type DeleteState,
} from "../lib/serviceActions";

interface DeleteServiceDialogProps {
  service: { id: string; name: string; serviceType: string };
  connectedNames: string[];
  // Called once the API accepted the delete (or said the service is gone).
  onDeleted: () => void;
  // Called when the user dismisses the dialog without deleting.
  onCancel: () => void;
}

const buttonBase: React.CSSProperties = {
  padding: "10px 14px",
  borderRadius: "8px",
  fontSize: "13px",
  fontWeight: 500,
  textAlign: "left",
};

export function DeleteServiceDialog({ service, connectedNames, onDeleted, onCancel }: DeleteServiceDialogProps) {
  const [state, setState] = useState<DeleteState>(openDelete());
  // Updated synchronously so two clicks in a row can never both send.
  const stateRef = useRef<DeleteState>(state);
  const mounted = useRef(true);
  const cancelRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    mounted.current = true;
    cancelRef.current?.focus();
    return () => {
      mounted.current = false;
    };
  }, []);

  const managed = isManagedDb(service.serviceType);
  const pending = state.phase === "pending";

  function dismiss() {
    const next = cancelDelete(stateRef.current);
    if (next.phase === "closed") onCancel();
  }

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
      const next = cancelDelete(stateRef.current);
      if (next.phase === "closed") onCancel();
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);

  async function submit(choice: DeleteChoice | null) {
    const step = beginDelete(stateRef.current, service.serviceType, choice);
    if (!step.send) return;
    stateRef.current = step.state;
    setState(step.state);
    try {
      await apiFetch(deletePath(service.id, step.purgeData), { method: "DELETE" });
      onDeleted();
    } catch (err) {
      const outcome = classifyDeleteError(err);
      if (outcome.kind === "done") {
        onDeleted();
        return;
      }
      const next = failDelete(stateRef.current, outcome.message);
      stateRef.current = next;
      if (mounted.current) setState(next);
    }
  }

  const disabledStyle: React.CSSProperties = pending ? { opacity: 0.6, cursor: "not-allowed" } : { cursor: "pointer" };

  return (
    <div
      style={{
        position: "fixed",
        inset: 0,
        zIndex: 150,
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        background: "rgba(0,0,0,0.6)",
        fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      }}
      onClick={dismiss}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-service-title"
        onClick={(e) => e.stopPropagation()}
        style={{
          width: "440px",
          maxWidth: "calc(100vw - 32px)",
          maxHeight: "calc(100vh - 32px)",
          overflow: "auto",
          boxSizing: "border-box",
          padding: "24px",
          background: "#111",
          border: "1px solid rgba(255,255,255,0.12)",
          borderRadius: "12px",
          boxShadow: "0 20px 60px rgba(0,0,0,0.6)",
          color: "#e5e7eb",
        }}
      >
        <h3 id="delete-service-title" style={{ margin: "0 0 12px 0", fontSize: "17px", color: "#fff", wordBreak: "break-word" }}>
          Delete “{service.name}”?
        </h3>
        <p style={{ margin: "0 0 12px 0", fontSize: "13px", lineHeight: 1.6, color: "#d1d5db" }}>
          Stops and removes the container and its URL. Removes env vars and connections. This cannot be undone.
          Services that use this one are not updated.
        </p>
        {connectedNames.length > 0 && (
          <p data-testid="connected" style={{ margin: "0 0 12px 0", fontSize: "13px", lineHeight: 1.6, color: "#fbbf24", wordBreak: "break-word" }}>
            {connectedNames.length === 1 ? "1 service is" : `${connectedNames.length} services are`} connected to this
            one and may break: {connectedNames.join(", ")}.
          </p>
        )}

        {state.phase === "confirm" && state.error && (
          <div
            role="alert"
            style={{
              margin: "0 0 12px 0",
              padding: "10px 12px",
              fontSize: "13px",
              color: "#fca5a5",
              background: "rgba(239,68,68,0.1)",
              border: "1px solid rgba(239,68,68,0.3)",
              borderRadius: "8px",
            }}
          >
            {state.error}
          </div>
        )}

        <div style={{ display: "flex", flexDirection: "column", gap: "10px", marginTop: "16px" }}>
          {managed ? (
            <>
              <p style={{ margin: 0, fontSize: "13px", color: "#9ca3af" }}>Choose what happens to the stored data:</p>
              <button
                data-testid="delete-purge"
                disabled={pending}
                onClick={() => void submit("purge")}
                style={{ ...buttonBase, ...disabledStyle, background: "rgba(239,68,68,0.15)", color: "#fca5a5", border: "1px solid rgba(239,68,68,0.4)" }}
              >
                Delete database and its data
                <span style={{ display: "block", marginTop: "4px", fontSize: "12px", fontWeight: 400, color: "#9ca3af" }}>
                  The data is erased for good, including data migrated from the earlier Infra Pilot version.
                </span>
              </button>
              <button
                data-testid="delete-keep"
                disabled={pending}
                onClick={() => void submit("keep")}
                style={{ ...buttonBase, ...disabledStyle, background: "rgba(255,255,255,0.05)", color: "#e5e7eb", border: "1px solid rgba(255,255,255,0.15)" }}
              >
                Delete service, keep data volume on disk
                <span style={{ display: "block", marginTop: "4px", fontSize: "12px", fontWeight: 400, color: "#9ca3af" }}>
                  The data stays on the server disk and is not reachable from Infra Pilot. The password is deleted, so
                  it cannot be restored.
                </span>
              </button>
            </>
          ) : (
            <button
              data-testid="delete-confirm"
              disabled={pending}
              onClick={() => void submit(null)}
              style={{ ...buttonBase, ...disabledStyle, textAlign: "center", background: "rgba(239,68,68,0.15)", color: "#fca5a5", border: "1px solid rgba(239,68,68,0.4)" }}
            >
              {pending ? "Deleting…" : "Delete service"}
            </button>
          )}
          <button
            ref={cancelRef}
            data-testid="delete-cancel"
            disabled={pending}
            onClick={dismiss}
            style={{ ...buttonBase, ...disabledStyle, textAlign: "center", background: "transparent", color: "#9ca3af", border: "1px solid rgba(255,255,255,0.1)" }}
          >
            Cancel
          </button>
          {pending && managed && (
            <p style={{ margin: 0, fontSize: "12px", color: "#9ca3af" }}>Deleting…</p>
          )}
        </div>
      </div>
    </div>
  );
}
