import { useEffect, useRef, useState } from "react";
import { apiFetch } from "../lib/api";
import {
  PURGE_ARM_MS, beginDelete, cancelDelete, classifyDeleteError, deletePath, disarmPurge, failDelete, isManagedDb,
  nextFocusIndex, openDelete, purgeButtonLabel,
  type ActivationInput, type DeleteChoice, type DeleteState,
} from "../lib/serviceActions";

interface DeleteServiceDialogProps {
  service: { id: string; name: string; serviceType: string };
  connectedNames: string[];
  // Called once the API accepted the delete (or said the service is gone).
  onDeleted: () => void;
  // Called when the user dismisses the dialog without deleting.
  onCancel: () => void;
  // Called with a readable message when the delete failed after this dialog
  // was already unmounted (nothing is left to show the error in).
  onFailed?: (message: string) => void;
}

const buttonBase: React.CSSProperties = {
  padding: "10px 14px",
  borderRadius: "8px",
  fontSize: "13px",
  fontWeight: 500,
  textAlign: "left",
};

export function DeleteServiceDialog({ service, connectedNames, onDeleted, onCancel, onFailed }: DeleteServiceDialogProps) {
  const [state, setState] = useState<DeleteState>(openDelete());
  // Updated synchronously so two clicks in a row can never both send.
  const stateRef = useRef<DeleteState>(state);
  const mounted = useRef(true);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  // The element that opened the dialog gets the focus back when it closes.
  const opener = useRef<Element | null>(document.activeElement);

  useEffect(() => {
    mounted.current = true;
    cancelRef.current?.focus();
    const from = opener.current;
    return () => {
      mounted.current = false;
      if (from instanceof HTMLElement && document.contains(from)) from.focus();
    };
  }, []);

  // Keep the focus inside the dialog: while pending every button is disabled,
  // so the dialog itself holds it; after an error it returns to Cancel.
  useEffect(() => {
    if (state.phase === "pending") {
      dialogRef.current?.focus();
    } else if (document.activeElement === document.body || document.activeElement === dialogRef.current) {
      cancelRef.current?.focus();
    }
  }, [state.phase]);

  const armed = state.phase === "confirm" && state.armed;
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => {
      const next = disarmPurge(stateRef.current);
      stateRef.current = next;
      setState(next);
    }, PURGE_ARM_MS);
    return () => clearTimeout(t);
  }, [armed]);

  useEffect(() => {
    function onTab(e: KeyboardEvent) {
      if (e.key !== "Tab" || !dialogRef.current) return;
      e.preventDefault();
      const items = Array.from(dialogRef.current.querySelectorAll<HTMLElement>("button:not([disabled])"));
      const i = nextFocusIndex(items.indexOf(document.activeElement as HTMLElement), items.length, e.shiftKey);
      (i < 0 ? dialogRef.current : items[i]).focus();
    }
    document.addEventListener("keydown", onTab);
    return () => document.removeEventListener("keydown", onTab);
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

  async function submit(choice: DeleteChoice | null, input?: ActivationInput) {
    const step = beginDelete(stateRef.current, service.serviceType, choice, input);
    if (step.state !== stateRef.current) {
      stateRef.current = step.state;
      setState(step.state);
    }
    if (!step.send) return;
    try {
      await apiFetch(deletePath(service.id, step.purgeData), { method: "DELETE" });
      onDeleted();
    } catch (err) {
      const outcome = classifyDeleteError(err);
      if (outcome.kind === "done") {
        onDeleted();
        return;
      }
      if (!mounted.current) {
        onFailed?.(outcome.message);
        return;
      }
      const next = failDelete(stateRef.current, outcome.message);
      stateRef.current = next;
      setState(next);
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
        ref={dialogRef}
        tabIndex={-1}
        aria-busy={pending}
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
          outline: "none",
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

        {pending && (
          <p
            role="status"
            data-testid="pending-text"
            style={{ margin: "0 0 12px 0", padding: "10px 12px", fontSize: "13px", color: "#ddd6fe", background: "rgba(139,92,246,0.12)", border: "1px solid rgba(139,92,246,0.3)", borderRadius: "8px" }}
          >
            Deleting “{service.name}”… this takes a few seconds. Keep this window open.
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
                onClick={(e) => void submit("purge", { now: performance.now(), detail: e.detail })}
                // A held Enter repeats keydown and would repeat the click.
                onKeyDown={(e) => {
                  if (e.repeat) e.preventDefault();
                }}
                style={{ ...buttonBase, ...disabledStyle, background: armed ? "rgba(239,68,68,0.45)" : "rgba(239,68,68,0.15)", color: armed ? "#fff" : "#fca5a5", border: "1px solid rgba(239,68,68,0.4)" }}
              >
                {purgeButtonLabel(state)}
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
        </div>
      </div>
    </div>
  );
}
