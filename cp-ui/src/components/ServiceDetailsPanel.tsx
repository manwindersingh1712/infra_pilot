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

  return (
    <div style={{ padding: "20px" }}>
      {error && (
        <div style={{
          background: "#fee2e2",
          color: "#991b1b",
          padding: "12px",
          borderRadius: "6px",
          marginBottom: "16px",
          fontSize: "14px"
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
            style={{
              flex: 1,
              padding: "10px 12px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              fontSize: "14px",
              fontFamily: "monospace"
            }}
          />
          <input
            type="text"
            placeholder="value"
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            style={{
              flex: 1.5,
              padding: "10px 12px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              fontSize: "14px"
            }}
          />
          <button
            type="submit"
            disabled={!newKey.trim()}
            style={{
              padding: "10px 20px",
              borderRadius: "6px",
              border: "none",
              background: "#3b82f6",
              color: "white",
              cursor: newKey.trim() ? "pointer" : "not-allowed",
              opacity: newKey.trim() ? 1 : 0.6,
              fontSize: "14px",
              fontWeight: 500
            }}
          >
            Add
          </button>
        </div>
      </form>

      {loading ? (
        <div style={{ textAlign: "center", padding: "40px", color: "#6b7280" }}>
          Loading...
        </div>
      ) : envVars.length === 0 ? (
        <div style={{
          textAlign: "center",
          padding: "40px",
          color: "#6b7280",
          background: "#f9fafb",
          borderRadius: "8px"
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
                background: "#f9fafb",
                borderRadius: "6px",
                border: "1px solid #e5e7eb"
              }}
            >
              <span style={{
                fontFamily: "monospace",
                fontSize: "14px",
                fontWeight: 600,
                color: "#111827",
                minWidth: "120px"
              }}>
                {env.key}
              </span>
              <span style={{
                flex: 1,
                fontSize: "14px",
                color: visibleValues.has(env.key) ? "#374151" : "#9ca3af",
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
                  border: "1px solid #d1d5db",
                  background: "white",
                  cursor: "pointer",
                  fontSize: "14px",
                  color: "#6b7280"
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
                  border: "1px solid #d1d5db",
                  background: "white",
                  cursor: "pointer",
                  fontSize: "12px",
                  color: "#991b1b"
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

  useEffect(() => {
    if (service && isOpen && activeTab === "deployments") {
      loadDeployments();
    }
  }, [service, isOpen, activeTab]);

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
  }, [service, isOpen, deployments]);

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
          background: "rgba(0,0,0,0.3)",
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
          background: "white",
          boxShadow: "-4px 0 20px rgba(0,0,0,0.15)",
          zIndex: 101,
          transform: isOpen ? "translateX(0)" : "translateX(100%)",
          transition: "transform 0.3s ease-out",
          display: "flex",
          flexDirection: "column"
        }}
      >
        {/* Header */}
        <div
          style={{
            padding: "20px 24px",
            borderBottom: "1px solid #e5e7eb",
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            background: "#f9fafb"
          }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
            <span style={{ fontSize: "24px" }}>{serviceIcons[service.serviceType] || "📦"}</span>
            <div>
              <h2 style={{ margin: 0, fontSize: "18px", fontWeight: 600 }}>{service.name}</h2>
              <p
                style={{
                  margin: "2px 0 0 0",
                  fontSize: "13px",
                  color: "#6b7280",
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
              color: "#6b7280",
              padding: "4px"
            }}
          >
            ×
          </button>
        </div>

        {/* Tabs */}
        <div style={{ display: "flex", borderBottom: "1px solid #e5e7eb" }}>
          <button
            onClick={() => setActiveTab("deployments")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "deployments" ? "white" : "#f3f4f6",
              border: "none",
              borderBottom: activeTab === "deployments" ? "2px solid #3b82f6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "deployments" ? "#3b82f6" : "#6b7280"
            }}
          >
            Deployments
          </button>
          <button
            onClick={() => setActiveTab("env")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "env" ? "white" : "#f3f4f6",
              border: "none",
              borderBottom: activeTab === "env" ? "2px solid #3b82f6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "env" ? "#3b82f6" : "#6b7280"
            }}
          >
            Env Vars
          </button>
          <button
            onClick={() => setActiveTab("logs")}
            style={{
              flex: 1,
              padding: "14px",
              background: activeTab === "logs" ? "white" : "#f3f4f6",
              border: "none",
              borderBottom: activeTab === "logs" ? "2px solid #3b82f6" : "none",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500,
              color: activeTab === "logs" ? "#3b82f6" : "#6b7280"
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
                    background: "#fee2e2",
                    color: "#991b1b",
                    padding: "12px",
                    borderRadius: "6px",
                    marginBottom: "16px",
                    fontSize: "14px"
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
                  background: "#3b82f6",
                  color: "white",
                  border: "none",
                  borderRadius: "6px",
                  cursor: "pointer",
                  fontSize: "14px",
                  fontWeight: 500,
                  marginBottom: "20px"
                }}
              >
                🚀 Deploy Latest
              </button>

              {loading ? (
                <div style={{ textAlign: "center", padding: "40px", color: "#6b7280" }}>
                  Loading deployments...
                </div>
              ) : deployments.length === 0 ? (
                <div
                  style={{
                    textAlign: "center",
                    padding: "40px",
                    color: "#6b7280",
                    background: "#f9fafb",
                    borderRadius: "8px"
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
                        border: "1px solid #e5e7eb",
                        background: "white"
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
                                ? "#dcfce7"
                                : deployment.status === "failed"
                                  ? "#fee2e2"
                                  : "#fef3c7",
                            color:
                              deployment.status === "deployed"
                                ? "#166534"
                                : deployment.status === "failed"
                                  ? "#991b1b"
                                  : "#92400e",
                            fontWeight: 500,
                            display: "flex",
                            alignItems: "center",
                            gap: "4px"
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

                      <div style={{ fontSize: "13px", marginBottom: "4px" }}>
                        <strong>Commit:</strong>{" "}
                        <span style={{ fontFamily: "monospace" }}>{deployment.commitSha.slice(0, 8)}</span>
                      </div>

                      {deployment.image && (
                        <div
                          style={{
                            fontSize: "13px",
                            marginBottom: "4px",
                            color: "#6b7280",
                            overflow: "hidden",
                            textOverflow: "ellipsis"
                          }}
                        >
                          <strong>Image:</strong> {deployment.image}
                        </div>
                      )}

                      {deployment.runtimeUrl && (
                        <div style={{ fontSize: "13px", marginBottom: "4px" }}>
                          <strong>URL:</strong>{" "}
                          <a
                            href={deployment.runtimeUrl}
                            target="_blank"
                            rel="noopener noreferrer"
                            style={{ color: "#3b82f6" }}
                          >
                            {deployment.runtimeUrl}
                          </a>
                        </div>
                      )}

                      <div style={{ fontSize: "12px", color: "#9ca3af", marginTop: "8px" }}>
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
                  <LogViewer deploymentId={latestDeploymentId} />
                </div>
              ) : (
                <div
                  style={{
                    textAlign: "center",
                    padding: "40px",
                    color: "#6b7280",
                    background: "#f9fafb",
                    borderRadius: "8px"
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
