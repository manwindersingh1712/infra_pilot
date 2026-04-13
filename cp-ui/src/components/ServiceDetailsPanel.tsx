import { useEffect, useState } from "react";
import { LogViewer } from "./LogViewer";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

interface Deployment {
  id: string;
  serviceId: string;
  commitSha: string;
  image: string | null;
  runtimeUrl: string | null;
  status: string;
  createdAt: string;
}

interface Service {
  id: string;
  name: string;
  serviceType: string;
}

interface EnvVar {
  id: string;
  key: string;
  value: string;
}

interface ServiceDetailsPanelProps {
  service: Service | null;
  isOpen: boolean;
  onClose: () => void;
}

function getToken() {
  return localStorage.getItem("cp_token") ?? "";
}

async function apiFetch<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const headers = new Headers(opts.headers);
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (opts.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(`${API}${path}`, { ...opts, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

function EnvVarSection({ serviceId }: { serviceId: string }) {
  const [envVars, setEnvVars] = useState<EnvVar[]>([]);
  const [loading, setLoading] = useState(false);
  const [newKey, setNewKey] = useState("");
  const [newValue, setNewValue] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [visibleValues, setVisibleValues] = useState<Set<string>>(new Set());

  useEffect(() => {
    loadEnvVars();
  }, [serviceId]);

  async function loadEnvVars() {
    setLoading(true);
    try {
      const data = await apiFetch<EnvVar[]>(`/services/${serviceId}/env`);
      setEnvVars(data);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  async function addEnvVar(e: React.FormEvent) {
    e.preventDefault();
    if (!newKey.trim()) return;
    try {
      await apiFetch(`/services/${serviceId}/env`, {
        method: "POST",
        body: JSON.stringify({ key: newKey, value: newValue })
      });
      setNewKey("");
      setNewValue("");
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message);
    }
  }

  async function deleteEnvVar(key: string) {
    try {
      await apiFetch(`/services/${serviceId}/env/${key}`, { method: "DELETE" });
      await loadEnvVars();
    } catch (err: any) {
      setError(err.message);
    }
  }

  function toggleValueVisibility(key: string) {
    setVisibleValues((prev) => {
      const newSet = new Set(prev);
      if (newSet.has(key)) {
        newSet.delete(key);
      } else {
        newSet.add(key);
      }
      return newSet;
    });
  }

  const inputStyle = {
    flex: 1,
    padding: "10px 12px",
    borderRadius: "6px",
    border: "1px solid rgba(255,255,255,0.1)",
    fontSize: "14px",
    background: "rgba(255,255,255,0.05)",
    color: "#fff",
    fontFamily: "monospace",
    outline: "none",
  };

  return (
    <div style={{ padding: "20px" }}>
      {error && (
        <div style={{
          background: "rgba(239,68,68,0.1)",
          color: "#ef4444",
          padding: "12px",
          borderRadius: "6px",
          marginBottom: "16px",
          fontSize: "14px",
          border: "1px solid rgba(239,68,68,0.2)",
        }}>
          {error}
        </div>
      )}

      <form onSubmit={addEnvVar} style={{ marginBottom: "20px" }}>
        <div style={{ display: "flex", gap: "8px", marginBottom: "12px" }}>
          <input
            type="text"
            placeholder="KEY"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            style={inputStyle}
          />
          <input
            type="text"
            placeholder="value"
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            style={{ ...inputStyle, flex: 1.5, fontFamily: "inherit" }}
          />
          <button
            type="submit"
            disabled={!newKey.trim()}
            style={{
              padding: "10px 20px",
              borderRadius: "6px",
              border: "none",
              background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
              color: "white",
              cursor: newKey.trim() ? "pointer" : "not-allowed",
              opacity: newKey.trim() ? 1 : 0.6,
              fontSize: "14px",
              fontWeight: 500,
            }}
          >
            Add
          </button>
        </div>
      </form>

      {loading ? (
        <div style={{ textAlign: "center", padding: "40px", color: "#9ca3af" }}>
          Loading...
        </div>
      ) : envVars.length === 0 ? (
        <div style={{
          textAlign: "center",
          padding: "40px",
          color: "#9ca3af",
          background: "rgba(255,255,255,0.03)",
          borderRadius: "8px",
          border: "1px solid rgba(255,255,255,0.1)",
        }}>
          <p>No environment variables set.</p>
          <p style={{ fontSize: "14px" }}>Add variables above to configure your service.</p>
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
          {envVars.map((env) => (
            <div
              key={env.key}
              style={{
                display: "flex",
                alignItems: "center",
                gap: "12px",
                padding: "12px 16px",
                background: "rgba(255,255,255,0.03)",
                borderRadius: "6px",
                border: "1px solid rgba(255,255,255,0.1)",
              }}
            >
              <span style={{
                fontFamily: "monospace",
                fontSize: "14px",
                fontWeight: 600,
                color: "#fff",
                minWidth: "120px"
              }}>
                {env.key}
              </span>
              <span style={{
                flex: 1,
                fontSize: "14px",
                color: visibleValues.has(env.key) ? "#e5e7eb" : "#6b7280",
                fontFamily: "monospace",
                overflow: "hidden",
                textOverflow: "ellipsis",
                userSelect: visibleValues.has(env.key) ? "text" : "none"
              }}>
                {visibleValues.has(env.key) ? env.value : "••••••••••"}
              </span>
              <button
                onClick={() => toggleValueVisibility(env.key)}
                style={{
                  padding: "6px 10px",
                  borderRadius: "4px",
                  border: "1px solid rgba(255,255,255,0.1)",
                  background: "rgba(255,255,255,0.05)",
                  cursor: "pointer",
                  fontSize: "14px",
                  color: "#9ca3af",
                  transition: "background 0.2s",
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = "rgba(255,255,255,0.1)";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = "rgba(255,255,255,0.05)";
                }}
                title={visibleValues.has(env.key) ? "Hide value" : "Show value"}
              >
                {visibleValues.has(env.key) ? "🙈" : "👁️"}
              </button>
              <button
                onClick={() => deleteEnvVar(env.key)}
                style={{
                  padding: "6px 12px",
                  borderRadius: "4px",
                  border: "1px solid rgba(239,68,68,0.3)",
                  background: "rgba(239,68,68,0.1)",
                  cursor: "pointer",
                  fontSize: "12px",
                  color: "#f87171",
                  transition: "background 0.2s",
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = "rgba(239,68,68,0.2)";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = "rgba(239,68,68,0.1)";
                }}
              >
                Delete
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

export function ServiceDetailsPanel({ service, isOpen, onClose }: ServiceDetailsPanelProps) {
  const [activeTab, setActiveTab] = useState<"deployments" | "logs" | "env">("deployments");
  const [deployments, setDeployments] = useState<Deployment[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [latestDeploymentId, setLatestDeploymentId] = useState<string | null>(null);

  // Reset deploymentId when service changes
  useEffect(() => {
    if (service?.id) {
      setLatestDeploymentId(null);
      setDeployments([]);
    }
  }, [service?.id]);

  // Load deployments whenever service changes or panel opens
  useEffect(() => {
    if (service && isOpen) {
      loadDeployments();
    }
  }, [service?.id, isOpen]);

  // Poll deployment status for non-terminal states
  useEffect(() => {
    if (!service || !isOpen) return;

    const hasPendingDeployments = deployments.some(
      (d) => d.status === "queued" || d.status === "building" || d.status === "deploying"
    );

    if (!hasPendingDeployments) return;

    const interval = setInterval(() => {
      loadDeployments();
    }, 2000);

    return () => clearInterval(interval);
  }, [service?.id, isOpen, deployments]);

  useEffect(() => {
    if (deployments.length > 0) {
      setLatestDeploymentId(deployments[0]?.id);
    }
  }, [deployments]);

  async function loadDeployments() {
    if (!service) return;
    setLoading(true);
    setError(null);
    try {
      const data = await apiFetch<Deployment[]>(`/deployments?serviceId=${service.id}`);
      setDeployments(data.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()));
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  async function triggerDeploy() {
    if (!service) return;
    try {
      setError(null);
      await apiFetch(`/services/${service.id}/deploy`, {
        method: "POST",
        body: JSON.stringify({ commitSha: "HEAD" })
      });
      await loadDeployments();
    } catch (err: any) {
      setError(err.message);
    }
  }

  function isDeploymentPending(status: string): boolean {
    return status === "queued" || status === "building" || status === "deploying";
  }

  if (!isOpen || !service) return null;

  const serviceIcons: Record<string, string> = {
    docker: "🐳",
    nodejs: "🟢",
    nextjs: "▲",
    react: "⚛️",
    mongodb: "🍃",
    redis: "🔴"
  };

  return (
    <>
      {/* Backdrop */}
      <div
        style={{
          position: "fixed",
          inset: 0,
          background: "rgba(0,0,0,0.5)",
          zIndex: 100,
          opacity: isOpen ? 1 : 0,
          pointerEvents: isOpen ? "auto" : "none",
          transition: "opacity 0.3s"
        }}
        onClick={onClose}
      />

      {/* Panel */}
      <div
        style={{
          position: "fixed",
          top: 0,
          right: 0,
          width: "60%",
          minWidth: "350px",
          maxWidth: "800px",
          height: "100vh",
          background: "#0a0a0a",
          boxShadow: "-4px 0 20px rgba(0,0,0,0.5)",
          zIndex: 101,
          transform: isOpen ? "translateX(0)" : "translateX(100%)",
          transition: "transform 0.3s ease-out",
          display: "flex",
          flexDirection: "column",
          fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
          borderLeft: "1px solid rgba(255,255,255,0.1)",
        }}
      >
        {/* Header */}
        <div
          style={{
            padding: "20px 24px",
            borderBottom: "1px solid rgba(255,255,255,0.1)",
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            background: "rgba(255,255,255,0.02)",
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
            <span style={{ fontSize: "24px" }}>{serviceIcons[service.serviceType] || "📦"}</span>
            <div>
              <h2 style={{ margin: 0, fontSize: "18px", fontWeight: 600, color: "#fff" }}>{service.name}</h2>
              <p
                style={{
                  margin: "2px 0 0 0",
                  fontSize: "13px",
                  color: "#9ca3af",
                  textTransform: "capitalize"
                }}
              >
                {service.serviceType}
              </p>
            </div>
          </div>
          <button
            onClick={onClose}
            style={{
              background: "none",
              border: "none",
              fontSize: "24px",
              cursor: "pointer",
              color: "#9ca3af",
              padding: "4px",
              borderRadius: "6px",
              transition: "background 0.2s, color 0.2s",
            }}
            onMouseEnter={(e) => {
              e.currentTarget.style.background = "rgba(255,255,255,0.05)";
              e.currentTarget.style.color = "#fff";
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.background = "transparent";
              e.currentTarget.style.color = "#9ca3af";
            }}
          >
            ×
          </button>
        </div>

        {/* Tabs */}
        <div style={{ display: "flex", borderBottom: "1px solid rgba(255,255,255,0.1)" }}>
          <button
            onClick={() => setActiveTab("deployments")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "deployments" ? "rgba(139,92,246,0.1)" : "transparent",
              border: "none",
              borderBottom: activeTab === "deployments" ? "2px solid #8b5cf6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "deployments" ? "#8b5cf6" : "#9ca3af",
              transition: "background 0.2s, color 0.2s",
            }}
            onMouseEnter={(e) => {
              if (activeTab !== "deployments") {
                e.currentTarget.style.background = "rgba(255,255,255,0.03)";
              }
            }}
            onMouseLeave={(e) => {
              if (activeTab !== "deployments") {
                e.currentTarget.style.background = "transparent";
              }
            }}
          >
            Deployments
          </button>
          <button
            onClick={() => setActiveTab("env")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "env" ? "rgba(139,92,246,0.1)" : "transparent",
              border: "none",
              borderBottom: activeTab === "env" ? "2px solid #8b5cf6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "env" ? "#8b5cf6" : "#9ca3af",
              transition: "background 0.2s, color 0.2s",
            }}
            onMouseEnter={(e) => {
              if (activeTab !== "env") {
                e.currentTarget.style.background = "rgba(255,255,255,0.03)";
              }
            }}
            onMouseLeave={(e) => {
              if (activeTab !== "env") {
                e.currentTarget.style.background = "transparent";
              }
            }}
          >
            Env Vars
          </button>
          <button
            onClick={() => setActiveTab("logs")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "logs" ? "rgba(139,92,246,0.1)" : "transparent",
              border: "none",
              borderBottom: activeTab === "logs" ? "2px solid #8b5cf6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "logs" ? "#8b5cf6" : "#9ca3af",
              transition: "background 0.2s, color 0.2s",
            }}
            onMouseEnter={(e) => {
              if (activeTab !== "logs") {
                e.currentTarget.style.background = "rgba(255,255,255,0.03)";
              }
            }}
            onMouseLeave={(e) => {
              if (activeTab !== "logs") {
                e.currentTarget.style.background = "transparent";
              }
            }}
          >
            Logs
          </button>
        </div>

        {/* Content */}
        <div style={{ flex: 1, overflow: "auto" }}>
          {activeTab === "deployments" ? (
            <div style={{ padding: "20px" }}>
              {error && (
                <div
                  style={{
                    background: "rgba(239,68,68,0.1)",
                    color: "#ef4444",
                    padding: "12px",
                    borderRadius: "6px",
                    marginBottom: "16px",
                    fontSize: "14px",
                    border: "1px solid rgba(239,68,68,0.2)",
                  }}
                >
                  {error}
                </div>
              )}

              <button
                onClick={triggerDeploy}
                style={{
                  width: "100%",
                  padding: "12px",
                  background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
                  color: "white",
                  border: "none",
                  borderRadius: "8px",
                  cursor: "pointer",
                  fontSize: "14px",
                  fontWeight: 500,
                  marginBottom: "20px",
                  transition: "box-shadow 0.2s",
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.boxShadow = "0 4px 20px rgba(139, 92, 246, 0.4)";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.boxShadow = "none";
                }}
              >
                🚀 Deploy Latest
              </button>

              {loading ? (
                <div style={{ textAlign: "center", padding: "40px", color: "#9ca3af" }}>
                  Loading deployments...
                </div>
              ) : deployments.length === 0 ? (
                <div
                  style={{
                    textAlign: "center",
                    padding: "40px",
                    color: "#9ca3af",
                    background: "rgba(255,255,255,0.03)",
                    borderRadius: "8px",
                    border: "1px solid rgba(255,255,255,0.1)",
                  }}
                >
                  <p>No deployments yet.</p>
                  <p style={{ fontSize: "14px" }}>Click "Deploy Latest" to create your first deployment.</p>
                </div>
              ) : (
                <div style={{ display: "flex", flexDirection: "column", gap: "12px" }}>
                  {deployments.map((deployment) => (
                    <div
                      key={deployment.id}
                      style={{
                        padding: "16px",
                        borderRadius: "8px",
                        border: "1px solid rgba(255,255,255,0.1)",
                        background: "rgba(255,255,255,0.03)",
                      }}
                    >
                      <div
                        style={{
                          display: "flex",
                          justifyContent: "space-between",
                          alignItems: "center",
                          marginBottom: "8px"
                        }}
                      >
                        <span
                          style={{
                            fontSize: "12px",
                            fontFamily: "monospace",
                            color: "#6b7280"
                          }}
                        >
                          {deployment.id.slice(0, 8)}
                        </span>
                        <span
                          style={{
                            fontSize: "12px",
                            padding: "4px 8px",
                            borderRadius: "4px",
                            background:
                              deployment.status === "deployed"
                                ? "rgba(34,197,94,0.1)"
                                : deployment.status === "failed"
                                  ? "rgba(239,68,68,0.1)"
                                  : "rgba(234,179,8,0.1)",
                            color:
                              deployment.status === "deployed"
                                ? "#22c55e"
                                : deployment.status === "failed"
                                  ? "#ef4444"
                                  : "#eab308",
                            fontWeight: 500,
                            display: "flex",
                            alignItems: "center",
                            gap: "4px",
                            border: `1px solid ${
                              deployment.status === "deployed"
                                ? "rgba(34,197,94,0.2)"
                                : deployment.status === "failed"
                                  ? "rgba(239,68,68,0.2)"
                                  : "rgba(234,179,8,0.2)"
                            }`,
                          }}
                        >
                          {isDeploymentPending(deployment.status) && (
                            <span
                              style={{
                                display: "inline-block",
                                width: "10px",
                                height: "10px",
                                border: "2px solid currentColor",
                                borderTopColor: "transparent",
                                borderRadius: "50%",
                                animation: "spin 1s linear infinite"
                              }}
                            />
                          )}
                          {deployment.status}
                        </span>
                      </div>

                      <div style={{ fontSize: "13px", marginBottom: "4px", color: "#e5e7eb" }}>
                        <strong style={{ color: "#9ca3af" }}>Commit:</strong>{" "}
                        <span style={{ fontFamily: "monospace" }}>{deployment.commitSha.slice(0, 8)}</span>
                      </div>

                      {deployment.image && (
                        <div
                          style={{
                            fontSize: "13px",
                            marginBottom: "4px",
                            color: "#9ca3af",
                            overflow: "hidden",
                            textOverflow: "ellipsis"
                          }}
                        >
                          <strong>Image:</strong> {deployment.image}
                        </div>
                      )}

                      {deployment.runtimeUrl && (
                        <div style={{ fontSize: "13px", marginBottom: "4px", color: "#e5e7eb" }}>
                          <strong style={{ color: "#9ca3af" }}>URL:</strong>{" "}
                          <a
                            href={deployment.runtimeUrl}
                            target="_blank"
                            rel="noopener noreferrer"
                            style={{ color: "#8b5cf6", textDecoration: "none" }}
                            onMouseEnter={(e) => {
                              e.currentTarget.style.textDecoration = "underline";
                            }}
                            onMouseLeave={(e) => {
                              e.currentTarget.style.textDecoration = "none";
                            }}
                          >
                            {deployment.runtimeUrl}
                          </a>
                        </div>
                      )}

                      <div style={{ fontSize: "12px", color: "#6b7280", marginTop: "8px" }}>
                        {new Date(deployment.createdAt).toLocaleString()}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          ) : activeTab === "env" ? (
            <EnvVarSection serviceId={service.id} />
          ) : (
            <div style={{ padding: "20px", height: "100%", boxSizing: "border-box" }}>
              {latestDeploymentId ? (
                <div style={{ height: "100%" }}>
                  <LogViewer key={latestDeploymentId} deploymentId={latestDeploymentId} />
                </div>
              ) : (
                <div
                  style={{
                    textAlign: "center",
                    padding: "40px",
                    color: "#9ca3af",
                    background: "rgba(255,255,255,0.03)",
                    borderRadius: "8px",
                    border: "1px solid rgba(255,255,255,0.1)",
                  }}
                >
                  <p>No deployments to show logs for.</p>
                  <p style={{ fontSize: "14px" }}>Deploy the service first to see logs.</p>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </>
  );
}
