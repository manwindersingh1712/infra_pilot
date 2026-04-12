import { useState } from "react";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

type ServiceType = "docker" | "nodejs" | "nextjs" | "react" | "mongodb" | "redis";

interface CreateServiceModalProps {
  projectId: string;
  isOpen: boolean;
  onClose: () => void;
  onCreated: () => void;
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

export function CreateServiceModal({ projectId, isOpen, onClose, onCreated }: CreateServiceModalProps) {
  const [name, setName] = useState("");
  const [serviceType, setServiceType] = useState<ServiceType>("docker");
  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!isOpen) return null;

  const needsRepo = serviceType === "docker" || serviceType === "nodejs" || serviceType === "nextjs" || serviceType === "react";

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) return;

    setLoading(true);
    setError(null);

    try {
      const body: any = {
        projectId,
        name,
        serviceType
      };

      if (needsRepo) {
        body.repoUrl = repoUrl;
        body.branch = branch;
      }

      // Create service
      const service = await apiFetch<{ id: string }>("/services", {
        method: "POST",
        body: JSON.stringify(body)
      });

      // Auto-deploy the service
      await apiFetch(`/services/${service.id}/deploy`, {
        method: "POST",
        body: JSON.stringify({ commitSha: "HEAD" })
      });

      // Reset form
      setName("");
      setServiceType("docker");
      setRepoUrl("");
      setBranch("main");

      onCreated();
      onClose();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  const inputStyle = {
    width: "100%",
    padding: "12px 16px",
    borderRadius: "8px",
    border: "1px solid rgba(255,255,255,0.1)",
    fontSize: "14px",
    boxSizing: "border-box" as const,
    background: "rgba(255,255,255,0.05)",
    color: "#fff",
    outline: "none",
    transition: "border-color 0.2s",
  };

  const labelStyle = {
    display: "block",
    fontSize: "14px",
    fontWeight: 500,
    marginBottom: "8px",
    color: "#e5e7eb",
  };

  return (
    <div style={{
      position: "fixed",
      inset: 0,
      background: "rgba(0,0,0,0.7)",
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      zIndex: 1000,
      fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
    }} onClick={onClose}>
      <div style={{
        background: "#111111",
        borderRadius: "16px",
        width: "100%",
        maxWidth: "500px",
        maxHeight: "90vh",
        overflow: "auto",
        border: "1px solid rgba(255,255,255,0.1)",
        boxShadow: "0 25px 50px -12px rgba(0,0,0,0.5)",
      }} onClick={(e) => e.stopPropagation()}>
        {/* Header */}
        <div style={{
          padding: "20px 24px",
          borderBottom: "1px solid rgba(255,255,255,0.1)",
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
        }}>
          <h2 style={{
            margin: 0,
            fontSize: "18px",
            fontWeight: 600,
            color: "#fff",
          }}>
            Create New Service
          </h2>
          <button
            onClick={onClose}
            style={{
              background: "none",
              border: "none",
              fontSize: "24px",
              cursor: "pointer",
              color: "#9ca3af",
              lineHeight: 1,
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

        {/* Form */}
        <form onSubmit={handleSubmit} style={{ padding: "24px" }}>
          {error && (
            <div style={{
              background: "rgba(239,68,68,0.1)",
              color: "#ef4444",
              padding: "12px 16px",
              borderRadius: "8px",
              marginBottom: "20px",
              fontSize: "14px",
              border: "1px solid rgba(239,68,68,0.2)",
            }}>
              {error}
            </div>
          )}

          <div style={{ marginBottom: "20px" }}>
            <label style={labelStyle}>
              Service Name
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="my-service"
              required
              style={inputStyle}
              onFocus={(e) => {
                e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
              }}
              onBlur={(e) => {
                e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
              }}
            />
          </div>

          <div style={{ marginBottom: "20px" }}>
            <label style={labelStyle}>
              Service Type
            </label>
            <select
              value={serviceType}
              onChange={(e) => setServiceType(e.target.value as ServiceType)}
              style={{
                ...inputStyle,
                cursor: "pointer",
                appearance: "none",
                backgroundImage: `url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12'%3E%3Cpath fill='%239ca3af' d='M6 8L1 3h10z'/%3E%3C/svg%3E")`,
                backgroundRepeat: "no-repeat",
                backgroundPosition: "right 16px center",
                paddingRight: "40px",
              }}
            >
              <option value="docker" style={{ background: "#1a1a1a" }}>Docker (with Dockerfile)</option>
              <option value="nodejs" style={{ background: "#1a1a1a" }}>Node.js (auto-detect)</option>
              <option value="nextjs" style={{ background: "#1a1a1a" }}>Next.js</option>
              <option value="react" style={{ background: "#1a1a1a" }}>React SPA</option>
              <option value="mongodb" style={{ background: "#1a1a1a" }}>MongoDB (managed)</option>
              <option value="redis" style={{ background: "#1a1a1a" }}>Redis (managed)</option>
            </select>
          </div>

          {needsRepo && (
            <>
              <div style={{ marginBottom: "20px" }}>
                <label style={labelStyle}>
                  Repository URL
                </label>
                <input
                  type="text"
                  value={repoUrl}
                  onChange={(e) => setRepoUrl(e.target.value)}
                  placeholder="https://github.com/username/repo"
                  required={needsRepo}
                  style={inputStyle}
                  onFocus={(e) => {
                    e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                  }}
                  onBlur={(e) => {
                    e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                  }}
                />
              </div>

              <div style={{ marginBottom: "24px" }}>
                <label style={labelStyle}>
                  Branch
                </label>
                <input
                  type="text"
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  placeholder="main"
                  style={inputStyle}
                  onFocus={(e) => {
                    e.currentTarget.style.borderColor = "rgba(139,92,246,0.5)";
                  }}
                  onBlur={(e) => {
                    e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                  }}
                />
              </div>
            </>
          )}

          <div style={{ display: "flex", gap: "12px", justifyContent: "flex-end" }}>
            <button
              type="button"
              onClick={onClose}
              style={{
                padding: "10px 20px",
                borderRadius: "8px",
                border: "1px solid rgba(255,255,255,0.1)",
                background: "rgba(255,255,255,0.05)",
                color: "#e5e7eb",
                cursor: "pointer",
                fontSize: "14px",
                fontWeight: 500,
                transition: "background 0.2s",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = "rgba(255,255,255,0.1)";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = "rgba(255,255,255,0.05)";
              }}
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={loading || !name.trim() || (needsRepo && !repoUrl.trim())}
              style={{
                padding: "10px 20px",
                borderRadius: "8px",
                border: "none",
                background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
                color: "white",
                cursor: loading ? "not-allowed" : "pointer",
                opacity: loading ? 0.6 : 1,
                fontSize: "14px",
                fontWeight: 500,
                transition: "box-shadow 0.2s, opacity 0.2s",
              }}
              onMouseEnter={(e) => {
                if (!loading) {
                  e.currentTarget.style.boxShadow = "0 4px 20px rgba(139, 92, 246, 0.4)";
                }
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.boxShadow = "none";
              }}
            >
              {loading ? "Creating..." : "Create Service"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
