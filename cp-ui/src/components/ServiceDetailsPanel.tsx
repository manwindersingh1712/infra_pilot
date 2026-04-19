import { useEffect, useState, ReactNode } from "react";
import { LogViewer } from "./LogViewer";
import { DeploymentStageTracker } from "./DeploymentStageTracker";

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
  const [expandedDeploymentIds, setExpandedDeploymentIds] = useState<Set<string>>(new Set());
  const [isHistoryExpanded, setIsHistoryExpanded] = useState(false);

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
      loadDeployments(false);
    }, 2000);

    return () => clearInterval(interval);
  }, [service?.id, isOpen, deployments]);

  useEffect(() => {
    if (deployments.length > 0) {
      setLatestDeploymentId(deployments[0]?.id);
    }
  }, [deployments]);

  async function loadDeployments(showLoading = true) {
    if (!service) return;
    if (showLoading) setLoading(true);
    setError(null);
    try {
      const data = await apiFetch<Deployment[]>(`/deployments?serviceId=${service.id}`);
      setDeployments(data.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime()));
    } catch (err: any) {
      setError(err.message);
    } finally {
      if (showLoading) setLoading(false);
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

  const serviceIcons: Record<string, string | ReactNode> = {
    docker: (
      <svg width="24" height="24" viewBox="0 0 32 32" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="16" cy="16" r="14" fill="#1794D4"/>
        <path d="M18 7H16V9H18V7Z" fill="white"/>
        <path d="M10 10H12V12H10V10Z" fill="white"/>
        <path d="M6.00155 16.9414C6.17244 19.8427 7.90027 24 14 24C20.8 24 23.8333 19 24.5 16.5C25.3333 16.5 27.2 16 28 14C27.5 13.5 25.5 13.5 24.5 14C24.5 13.2 24 11.5 23 11C22.3333 11.6667 21.3 13.4 22.5 15C22 16 20.6667 16 20 16H6.9429C6.41342 16 5.97041 16.4128 6.00155 16.9414Z" fill="white"/>
        <path d="M9 13H7V15H9V13Z" fill="white"/>
        <path d="M10 13H12V15H10V13Z" fill="white"/>
        <path d="M15 13H13V15H15V13Z" fill="white"/>
        <path d="M16 13H18V15H16V13Z" fill="white"/>
        <path d="M21 13H19V15H21V13Z" fill="white"/>
        <path d="M15 10H13V12H15V10Z" fill="white"/>
        <path d="M16 10H18V12H16V10Z" fill="white"/>
      </svg>
    ),
    nodejs: (
      <svg width="24" height="24" viewBox="-16.5 0 289 289" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path d="M127.999999,288.463771 C124.024844,288.463771 120.314699,287.403728 116.869564,285.548656 L81.6231884,264.612838 C76.32298,261.697724 78.9730854,260.637682 80.5631458,260.107661 C87.7184259,257.72257 89.0434775,257.192547 96.4637688,252.952381 C97.2587979,252.422361 98.3188405,252.687372 99.1138718,253.217392 L126.144927,269.383024 C127.20497,269.913045 128.530021,269.913045 129.325053,269.383024 L235.064182,208.165634 C236.124225,207.635611 236.654245,206.575571 236.654245,205.250519 L236.654245,83.0807467 C236.654245,81.7556929 236.124225,80.6956526 235.064182,80.1656324 L129.325053,19.2132506 C128.26501,18.6832305 126.939959,18.6832305 126.144927,19.2132506 L20.4057954,80.1656324 C19.3457551,80.6956526 18.8157349,82.0207041 18.8157349,83.0807467 L18.8157349,205.250519 C18.8157349,206.31056 19.3457551,207.635611 20.4057954,208.165634 L49.2919247,224.861286 C64.9275364,232.811595 74.7329196,223.536234 74.7329196,214.260871 L74.7329196,93.681159 C74.7329196,92.0910985 76.0579711,90.5010358 77.9130428,90.5010358 L91.4285716,90.5010358 C93.0186343,90.5010358 94.6086948,91.8260873 94.6086948,93.681159 L94.6086948,214.260871 C94.6086948,235.196689 83.2132512,247.387164 63.3374737,247.387164 C57.2422362,247.387164 52.4720502,247.387164 38.9565214,240.761906 L11.1304347,224.861286 C4.24016581,220.886129 5.68434189e-14,213.46584 5.68434189e-14,205.515528 L5.68434189e-14,83.3457557 C5.68434189e-14,75.3954465 4.24016581,67.9751552 11.1304347,64.0000006 L116.869564,2.78260752 C123.494824,-0.927535841 132.505176,-0.927535841 139.130436,2.78260752 L244.869565,64.0000006 C251.759834,67.9751552 256,75.3954465 256,83.3457557 L256,205.515528 C256,213.46584 251.759834,220.886129 244.869565,224.861286 L139.130436,286.078676 C135.685299,287.668739 131.710145,288.463771 127.999999,288.463771 L127.999999,288.463771 Z M160.596274,204.455488 C114.219461,204.455488 104.679089,183.254659 104.679089,165.233955 C104.679089,163.643893 106.004141,162.053832 107.859212,162.053832 L121.639752,162.053832 C123.229813,162.053832 124.554864,163.113872 124.554864,164.703935 C126.674947,178.749484 132.770187,185.639753 160.861283,185.639753 C183.122154,185.639753 192.662526,180.604556 192.662526,168.67909 C192.662526,161.788821 190.012423,156.753624 155.296065,153.308489 C126.409938,150.393375 108.389235,144.033126 108.389235,120.977226 C108.389235,99.5113875 126.409938,86.7908901 156.621119,86.7908901 C190.542443,86.7908901 207.238095,98.4513472 209.358178,123.89234 C209.358178,124.687371 209.093167,125.482403 208.563147,126.277434 C208.033127,126.807454 207.238095,127.337474 206.443064,127.337474 L192.662526,127.337474 C191.337475,127.337474 190.012423,126.277434 189.747412,124.952382 C186.567289,110.376813 178.351966,105.606625 156.621119,105.606625 C132.240165,105.606625 129.325053,114.086957 129.325053,120.447205 C129.325053,128.132506 132.770187,130.5176 165.631471,134.757766 C198.227744,138.997931 213.598344,145.093169 213.598344,167.884058 C213.333333,191.20497 194.252589,204.455488 160.596274,204.455488 L160.596274,204.455488 Z" fill="#539E43"/>
      </svg>
    ),
    nextjs: (
      <svg width="24" height="24" viewBox="0 0 15 15" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path fill-rule="evenodd" clip-rule="evenodd" d="M0 7.5C0 3.35786 3.35786 0 7.5 0C11.6421 0 15 3.35786 15 7.5C15 10.087 13.6902 12.3681 11.6975 13.7163L4.90687 4.20942C4.78053 4.03255 4.5544 3.95756 4.34741 4.02389C4.14042 4.09022 4 4.28268 4 4.50004V12H5V6.06027L10.8299 14.2221C9.82661 14.7201 8.696 15 7.5 15C3.35786 15 0 11.6421 0 7.5ZM10 10V4H11V10H10Z" fill="#ffffff"/>
      </svg>
    ),
    react: "⚛️",
    mongodb: (
      <svg width="24" height="24" viewBox="0 0 1024 1024" fill="none" xmlns="http://www.w3.org/2000/svg">
        <circle cx="512" cy="512" r="512" fill="#13aa52"/>
        <path d="M648.86 449.44c-32.34-142.73-108.77-189.66-117-207.59-9-12.65-18.12-35.15-18.12-35.15-.15-.38-.39-1.05-.67-1.7-.93 12.65-1.41 17.53-13.37 30.29-18.52 14.48-113.54 94.21-121.27 256.37-7.21 151.24 109.25 241.36 125 252.85l1.79 1.27v-.11c.1.76 5 36 8.44 73.34H526a726.68 726.68 0 0 1 13-78.53l1-.65a204.48 204.48 0 0 0 20.11-16.45l.72-.65c33.48-30.93 93.67-102.47 93.08-216.53a347.07 347.07 0 0 0-5.05-56.76zM512.35 659.12s0-212.12 7-212.08c5.46 0 12.53 273.61 12.53 273.61-9.72-1.17-19.53-45.03-19.53-61.53z" fill="#fff"/>
      </svg>
    ),
    redis: (
      <svg width="24" height="24" viewBox="0 0 256 256" fill="none" xmlns="http://www.w3.org/2000/svg">
        <path d="M245.97 168.943c-13.662 7.121-84.434 36.22-99.501 44.075-15.067 7.856-23.437 7.78-35.34 2.09-11.902-5.69-87.216-36.112-100.783-42.597C3.566 169.271 0 166.535 0 163.951v-25.876s98.05-21.345 113.879-27.024c15.828-5.679 21.32-5.884 34.79-.95 13.472 4.936 94.018 19.468 107.331 24.344l-.006 25.51c.002 2.558-3.07 5.364-10.024 8.988" fill="#912626"/>
        <path d="M245.965 143.22c-13.661 7.118-84.431 36.218-99.498 44.072-15.066 7.857-23.436 7.78-35.338 2.09-11.903-5.686-87.214-36.113-100.78-42.594-13.566-6.485-13.85-10.948-.524-16.166 13.326-5.22 88.224-34.605 104.055-40.284 15.828-5.677 21.319-5.884 34.789-.948 13.471 4.934 83.819 32.935 97.13 37.81 13.316 4.881 13.827 8.9.166 16.02" fill="#C6302B"/>
        <path d="M245.97 127.074c-13.662 7.122-84.434 36.22-99.501 44.078-15.067 7.853-23.437 7.777-35.34 2.087-11.903-5.687-87.216-36.112-100.783-42.597C3.566 127.402 0 124.67 0 122.085V96.206s98.05-21.344 113.879-27.023c15.828-5.679 21.32-5.885 34.79-.95C162.142 73.168 242.688 87.697 256 92.574l-.006 25.513c.002 2.557-3.07 5.363-10.024 8.987" fill="#912626"/>
        <path d="M245.965 101.351c-13.661 7.12-84.431 36.218-99.498 44.075-15.066 7.854-23.436 7.777-35.338 2.087-11.903-5.686-87.214-36.112-100.78-42.594-13.566-6.483-13.85-10.947-.524-16.167C23.151 83.535 98.05 54.148 113.88 48.47c15.828-5.678 21.319-5.884 34.789-.949 13.471 4.934 83.819 32.933 97.13 37.81 13.316 4.88 13.827 8.9.166 16.02" fill="#C6302B"/>
        <path d="M245.97 83.653c-13.662 7.12-84.434 36.22-99.501 44.078-15.067 7.854-23.437 7.777-35.34 2.087-11.903-5.687-87.216-36.113-100.783-42.595C3.566 83.98 0 81.247 0 78.665v-25.88s98.05-21.343 113.879-27.021c15.828-5.68 21.32-5.884 34.79-.95C162.142 29.749 242.688 44.278 256 49.155l-.006 25.512c.002 2.555-3.07 5.361-10.024 8.986" fill="#912626"/>
        <path d="M245.965 57.93c-13.661 7.12-84.431 36.22-99.498 44.074-15.066 7.854-23.436 7.777-35.338 2.09C99.227 98.404 23.915 67.98 10.35 61.497-3.217 55.015-3.5 50.55 9.825 45.331 23.151 40.113 98.05 10.73 113.88 5.05c15.828-5.679 21.319-5.883 34.789-.948 13.471 4.935 83.819 32.934 97.13 37.811 13.316 4.876 13.827 8.897.166 16.017" fill="#C6302B"/>
        <path d="M159.283 32.757l-22.01 2.285-4.927 11.856-7.958-13.23-25.415-2.284 18.964-6.839-5.69-10.498 17.755 6.944 16.738-5.48-4.524 10.855 17.067 6.391M131.032 90.275L89.955 73.238l58.86-9.035-17.783 26.072M74.082 39.347c17.375 0 31.46 5.46 31.46 12.194 0 6.736-14.085 12.195-31.46 12.195s-31.46-5.46-31.46-12.195c0-6.734 14.085-12.194 31.46-12.194" fill="#FFF"/>
        <path d="M185.295 35.998l34.836 13.766-34.806 13.753-.03-27.52" fill="#621B1C"/>
        <path d="M146.755 51.243l38.54-15.245.03 27.519-3.779 1.478-34.791-13.752" fill="#9A2928"/>
      </svg>
    )
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
            <span style={{ fontSize: "24px", display: "flex", alignItems: "center" }}>{serviceIcons[service.serviceType] || "📦"}</span>
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
                  {/* Current Deployment */}
                  <DeploymentStageTracker
                    key={deployments[0].id}
                    deployment={deployments[0]}
                    isExpanded={expandedDeploymentIds.has(deployments[0].id)}
                    onToggle={() => {
                      setExpandedDeploymentIds(prev => {
                        const next = new Set(prev);
                        if (next.has(deployments[0].id)) {
                          next.delete(deployments[0].id);
                        } else {
                          next.add(deployments[0].id);
                        }
                        return next;
                      });
                    }}
                    onViewLogs={(id) => {
                      setLatestDeploymentId(id);
                      setActiveTab("logs");
                    }}
                  />

                  {/* Previous Deployment - shown only if current is not deployed */}
                  {deployments.length > 1 && deployments[0].status !== "deployed" && (
                    <DeploymentStageTracker
                      key={deployments[1].id}
                      deployment={deployments[1]}
                      isExpanded={expandedDeploymentIds.has(deployments[1].id)}
                      onToggle={() => {
                        setExpandedDeploymentIds(prev => {
                          const next = new Set(prev);
                          if (next.has(deployments[1].id)) {
                            next.delete(deployments[1].id);
                          } else {
                            next.add(deployments[1].id);
                          }
                          return next;
                        });
                      }}
                      onViewLogs={(id) => {
                        setLatestDeploymentId(id);
                        setActiveTab("logs");
                      }}
                    />
                  )}

                  {/* History - show deployments starting from index 2 if current not deployed, else from index 1 */}
                  {deployments.length > (deployments[0].status === "deployed" ? 1 : 2) && (
                    <div style={{ marginTop: "8px" }}>
                      <div
                        onClick={() => setIsHistoryExpanded(!isHistoryExpanded)}
                        style={{
                          display: "flex",
                          alignItems: "center",
                          gap: "8px",
                          padding: "12px 16px",
                          cursor: "pointer",
                          color: "#9ca3af",
                          fontSize: "13px",
                          fontWeight: 500,
                          textTransform: "uppercase",
                          letterSpacing: "0.5px",
                          borderTop: "1px solid rgba(255, 255, 255, 0.05)",
                        }}
                      >
                        <span
                          style={{
                            transform: isHistoryExpanded ? "rotate(90deg)" : "rotate(0deg)",
                            transition: "transform 0.2s",
                            fontSize: "10px",
                          }}
                        >
                          ▶
                        </span>
                        History
                      </div>

                      {isHistoryExpanded && (
                        <div style={{ display: "flex", flexDirection: "column", gap: "8px", paddingTop: "8px" }}>
                          {deployments
                            .slice(deployments[0].status === "deployed" ? 1 : 2)
                            .map((deployment) => (
                              <div
                                key={deployment.id}
                                style={{
                                  padding: "16px",
                                  borderRadius: "8px",
                                  border: "1px solid rgba(255, 255, 255, 0.1)",
                                  background: "rgba(255, 255, 255, 0.03)",
                                  display: "flex",
                                  alignItems: "center",
                                  justifyContent: "space-between",
                                }}
                              >
                                <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
                                  <span
                                    style={{
                                      fontSize: "12px",
                                      padding: "4px 10px",
                                      borderRadius: "4px",
                                      background: "rgba(107, 114, 128, 0.2)",
                                      color: "#9ca3af",
                                      fontWeight: 600,
                                      textTransform: "uppercase",
                                    }}
                                  >
                                    REMOVED
                                  </span>
                                  <div>
                                    <div style={{ fontSize: "14px", color: "#e5e7eb", fontWeight: 500 }}>
                                      {deployment.commitSha.slice(0, 8)}
                                    </div>
                                    <div style={{ fontSize: "12px", color: "#6b7280" }}>
                                      {new Date(deployment.createdAt).toLocaleString()}
                                    </div>
                                  </div>
                                </div>
                              </div>
                            ))}
                        </div>
                      )}
                    </div>
                  )}
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
