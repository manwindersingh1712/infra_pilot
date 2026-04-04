import { useEffect, useMemo, useState } from "react";
import { LogViewer } from "./components/LogViewer";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

type Project = { id: string; name: string; createdAt: string };
type Service = { id: string; projectId: string; name: string; serviceType: string; repoUrl: string | null; branch: string; createdAt: string };
type Deployment = { id: string; serviceId: string; commitSha: string; image: string | null; runtimeUrl: string | null; status: string; createdAt: string };

type ServiceType = "docker" | "nodejs" | "react" | "mongodb" | "redis";

async function apiFetch<T>(path: string, opts: RequestInit = {}, token?: string): Promise<T> {
  const headers = new Headers(opts.headers);

  if (token) headers.set("Authorization", `Bearer ${token}`);

  if (opts.body != null && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(`${API}${path}`, {
    ...opts,
    headers
  });

  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status} ${res.statusText}: ${text}`);
  }
  return res.json();
}

export default function App() {
  const [token, setToken] = useState<string>(() => localStorage.getItem("cp_token") ?? "");
  const [err, setErr] = useState<string>("");

  // create forms
  const [projectName, setProjectName] = useState("infra-pilot");
  const [serviceName, setServiceName] = useState("svc1");
  const [serviceType, setServiceType] = useState<ServiceType>("docker");
  const [repoUrl, setRepoUrl] = useState("https://github.com/<youruser>/<yourrepo>");
  const [branch, setBranch] = useState("main");
  const [commitSha, setCommitSha] = useState("abc1234");

  // env vars
  const [envVars, setEnvVars] = useState<{ id: string; key: string; value: string }[]>([]);
  const [newEnvKey, setNewEnvKey] = useState("");
  const [newEnvValue, setNewEnvValue] = useState("");

  // data
  const [projects, setProjects] = useState<Project[]>([]);
  const [services, setServices] = useState<Service[]>([]);
  const [deployments, setDeployments] = useState<Deployment[]>([]);

  const [selectedProjectId, setSelectedProjectId] = useState<string>("");
  const [selectedServiceId, setSelectedServiceId] = useState<string>("");
  const [selectedDeploymentId, setSelectedDeploymentId] = useState<string>("");

  const authed = useMemo(() => Boolean(token), [token]);

  async function login() {
    setErr("");
    const r = await apiFetch<{ token: string }>(`/dev/login`, { method: "POST" });
    localStorage.setItem("cp_token", r.token);
    setToken(r.token);
  }

  function logout() {
    localStorage.removeItem("cp_token");
    setToken("");
    setProjects([]);
    setServices([]);
    setDeployments([]);
  }

  async function loadProjects() {
    if (!token) return;
    const data = await apiFetch<Project[]>("/projects", {}, token);
    setProjects(data);
    if (!selectedProjectId && data[0]?.id) setSelectedProjectId(data[0].id);
  }

  async function loadServices(projectId?: string) {
    if (!token) return;
    const q = projectId ? `?projectId=${encodeURIComponent(projectId)}` : "";
    const data = await apiFetch<Service[]>(`/services${q}`, {}, token);
    setServices(data);
    if (!selectedServiceId && data[0]?.id) setSelectedServiceId(data[0].id);
  }

  async function loadDeployments(serviceId?: string) {
    if (!token) return;
    const q = serviceId ? `?serviceId=${encodeURIComponent(serviceId)}` : "";
    const data = await apiFetch<Deployment[]>(`/deployments${q}`, {}, token);
    setDeployments(data);
  }

  async function loadEnvVars(serviceId: string) {
    if (!token) return;
    const data = await apiFetch<{ id: string; key: string; value: string }[]>(`/services/${serviceId}/env`, {}, token);
    setEnvVars(data);
  }

  async function addEnvVar() {
    if (!token || !selectedServiceId) return;
    if (!newEnvKey.trim()) return;
    await apiFetch(`/services/${selectedServiceId}/env`, {
      method: "POST",
      body: JSON.stringify({ key: newEnvKey, value: newEnvValue })
    }, token);
    setNewEnvKey("");
    setNewEnvValue("");
    await loadEnvVars(selectedServiceId);
  }

  async function deleteEnvVar(key: string) {
    if (!token || !selectedServiceId) return;
    await apiFetch(`/services/${selectedServiceId}/env/${encodeURIComponent(key)}`, {
      method: "DELETE"
    }, token);
    await loadEnvVars(selectedServiceId);
  }

  async function createProject() {
    setErr("");
    const p = await apiFetch<Project>(
      "/projects",
      { method: "POST", body: JSON.stringify({ name: projectName }) },
      token
    );
    await loadProjects();
    setSelectedProjectId(p.id);
  }

  async function createService() {
    setErr("");
    if (!selectedProjectId) throw new Error("Select a project first");

    const body: any = {
      projectId: selectedProjectId,
      name: serviceName,
      serviceType
    };

    // Only include repoUrl and branch for docker/nodejs/react types
    if (serviceType === "docker" || serviceType === "nodejs" || serviceType === "nextjs" || serviceType === "react") {
      body.repoUrl = repoUrl;
      body.branch = branch;
    }

    const s = await apiFetch<Service>(
      "/services",
      {
        method: "POST",
        body: JSON.stringify(body)
      },
      token
    );
    await loadServices(selectedProjectId);
    setSelectedServiceId(s.id);
  }

  async function triggerDeploy() {
    setErr("");
    if (!selectedServiceId) throw new Error("Select a service first");

    await apiFetch<{ deploymentId: string; status: string }>(
      `/services/${selectedServiceId}/deploy`,
      { method: "POST", body: JSON.stringify({ commitSha }) },
      token
    );

    await loadDeployments(selectedServiceId);
  }

  // initial load once authed
  useEffect(() => {
    (async () => {
      try {
        if (!token) return;
        await loadProjects();
      } catch (e: any) {
        setErr(e.message ?? String(e));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  // when project changes, load services
  useEffect(() => {
    (async () => {
      try {
        if (!token) return;
        if (!selectedProjectId) return;
        await loadServices(selectedProjectId);
      } catch (e: any) {
        setErr(e.message ?? String(e));
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedProjectId, token]);

  // load env vars when service changes
  useEffect(() => {
    if (!token || !selectedServiceId) {
      setEnvVars([]);
      return;
    }
    loadEnvVars(selectedServiceId).catch((e: any) => setErr(e.message ?? String(e)));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedServiceId, token]);

  // poll deployments every 2s for selected service
  useEffect(() => {
    if (!token || !selectedServiceId) return;

    let stop = false;
    const tick = async () => {
      try {
        await loadDeployments(selectedServiceId);
      } catch (e: any) {
        if (!stop) setErr(e.message ?? String(e));
      }
    };

    tick();
    const id = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedServiceId, token]);

  return (
    <div style={{ fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, Arial", padding: 16, maxWidth: 1100, margin: "0 auto" }}>
      <h2>Control Plane UI</h2>

      <div style={{ display: "flex", gap: 12, alignItems: "center", marginBottom: 12 }}>
        <span><b>API:</b> {API}</span>
        <span>•</span>
        <span><b>Auth:</b> {authed ? "✅ logged in" : "❌ not logged in"}</span>

        <div style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
          {!authed ? (
            <button onClick={() => login().catch(e => setErr(e.message))}>Login (dev)</button>
          ) : (
            <button onClick={logout}>Logout</button>
          )}
          <button onClick={() => window.location.reload()}>Reload UI</button>
        </div>
      </div>

      {err ? (
        <div style={{ background: "#fee", border: "1px solid #f99", padding: 10, marginBottom: 12, whiteSpace: "pre-wrap" }}>
          <b>Error:</b> {err}
        </div>
      ) : null}

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16 }}>
        <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8 }}>
          <h3>Projects</h3>

          <div style={{ display: "flex", gap: 8, marginBottom: 10 }}>
            <input value={projectName} onChange={(e) => setProjectName(e.target.value)} placeholder="project name" style={{ flex: 1 }} />
            <button disabled={!authed} onClick={() => createProject().catch(e => setErr(e.message))}>Create</button>
          </div>

          <select
            value={selectedProjectId}
            onChange={(e) => setSelectedProjectId(e.target.value)}
            style={{ width: "100%", padding: 8 }}
            disabled={!authed}
          >
            <option value="">-- select project --</option>
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} ({p.id.slice(0, 6)})
              </option>
            ))}
          </select>

          <div style={{ marginTop: 8 }}>
            <button disabled={!authed} onClick={() => loadProjects().catch(e => setErr(e.message))}>Refresh Projects</button>
          </div>
        </section>

        <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8 }}>
          <h3>Services</h3>

          <div style={{ display: "grid", gap: 8, marginBottom: 10 }}>
            <input value={serviceName} onChange={(e) => setServiceName(e.target.value)} placeholder="service name" />
            <select
              value={serviceType}
              onChange={(e) => setServiceType(e.target.value as ServiceType)}
              style={{ padding: 8 }}
            >
              <option value="docker">Docker (with Dockerfile)</option>
              <option value="nodejs">Node.js (auto-detect)</option>
              <option value="react">React SPA (Vite/CRA)</option>
              <option value="mongodb">MongoDB</option>
              <option value="redis">Redis</option>
            </select>
            {(serviceType === "docker" || serviceType === "nodejs" || serviceType === "nextjs" || serviceType === "react") && (
              <>
                <input value={repoUrl} onChange={(e) => setRepoUrl(e.target.value)} placeholder="repo url" />
                <input value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="branch" />
              </>
            )}
            <button disabled={!authed || !selectedProjectId} onClick={() => createService().catch(e => setErr(e.message))}>
              Create Service in Project
            </button>
          </div>

          <select
            value={selectedServiceId}
            onChange={(e) => setSelectedServiceId(e.target.value)}
            style={{ width: "100%", padding: 8 }}
            disabled={!authed}
          >
            <option value="">-- select service --</option>
            {services.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({s.id.slice(0, 6)}) • {s.serviceType}
              </option>
            ))}
          </select>

          <div style={{ marginTop: 8 }}>
            <button disabled={!authed} onClick={() => loadServices(selectedProjectId).catch(e => setErr(e.message))}>Refresh Services</button>
          </div>
        </section>
      </div>

      <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8, marginTop: 16 }}>
        <h3>Deploy</h3>

        <div style={{ display: "flex", gap: 8, marginBottom: 10 }}>
          <input value={commitSha} onChange={(e) => setCommitSha(e.target.value)} placeholder="commit sha" style={{ flex: 1 }} />
          <button disabled={!authed || !selectedServiceId} onClick={() => triggerDeploy().catch(e => setErr(e.message))}>
            Trigger Deploy
          </button>
          <button disabled={!authed || !selectedServiceId} onClick={() => loadDeployments(selectedServiceId).catch(e => setErr(e.message))}>
            Refresh Deployments
          </button>
        </div>

        <div style={{ fontSize: 12, color: "#555", marginBottom: 8 }}>
          Polling deployments every 2s for selected service.
        </div>

        <div style={{ overflowX: "auto" }}>
          <table style={{ width: "100%", borderCollapse: "collapse" }}>
            <thead>
              <tr>
                {["id", "status", "commitSha", "image", "runtimeUrl", "createdAt"].map((h) => (
                  <th key={h} style={{ textAlign: "left", borderBottom: "1px solid #ddd", padding: 8 }}>
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {deployments.map((d) => (
                <tr
                  key={d.id}
                  onClick={() => setSelectedDeploymentId(d.id)}
                  style={{
                    cursor: "pointer",
                    background: selectedDeploymentId === d.id ? "#eff6ff" : undefined
                  }}
                >
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>{d.id}</td>
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8 }}>{d.status}</td>
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>{d.commitSha}</td>
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>{d.image ?? "-"}</td>
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>
                    {d.runtimeUrl ? (
                      <a href={d.runtimeUrl} target="_blank" rel="noopener noreferrer">{d.runtimeUrl}</a>
                    ) : "-"}
                  </td>
                  <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8 }}>{new Date(d.createdAt).toLocaleString()}</td>
                </tr>
              ))}
              {deployments.length === 0 ? (
                <tr>
                  <td colSpan={6} style={{ padding: 12, color: "#777" }}>
                    No deployments yet.
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      </section>

      <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8, marginTop: 16 }}>
        <h3>Environment Variables</h3>

        <div style={{ display: "flex", gap: 8, marginBottom: 10 }}>
          <input
            value={newEnvKey}
            onChange={(e) => setNewEnvKey(e.target.value)}
            placeholder="KEY"
            style={{ flex: 1 }}
            disabled={!authed || !selectedServiceId}
          />
          <input
            value={newEnvValue}
            onChange={(e) => setNewEnvValue(e.target.value)}
            placeholder="value"
            style={{ flex: 2 }}
            disabled={!authed || !selectedServiceId}
          />
          <button disabled={!authed || !selectedServiceId || !newEnvKey.trim()} onClick={() => addEnvVar().catch(e => setErr(e.message))}>
            Add
          </button>
        </div>

        <div style={{ overflowX: "auto" }}>
          {envVars.length === 0 ? (
            <div style={{ color: "#777", fontSize: 14 }}>No environment variables set.</div>
          ) : (
            <table style={{ width: "100%", borderCollapse: "collapse" }}>
              <thead>
                <tr>
                  <th style={{ textAlign: "left", borderBottom: "1px solid #ddd", padding: 8 }}>Key</th>
                  <th style={{ textAlign: "left", borderBottom: "1px solid #ddd", padding: 8 }}>Value</th>
                  <th style={{ textAlign: "right", borderBottom: "1px solid #ddd", padding: 8 }}>Action</th>
                </tr>
              </thead>
              <tbody>
                {envVars.map((ev) => (
                  <tr key={ev.id}>
                    <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>{ev.key}</td>
                    <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, fontFamily: "monospace" }}>{ev.value}</td>
                    <td style={{ borderBottom: "1px solid #f0f0f0", padding: 8, textAlign: "right" }}>
                      <button onClick={() => deleteEnvVar(ev.key).catch(e => setErr(e.message))} disabled={!authed}>
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </section>

      {selectedDeploymentId && (
        <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8, marginTop: 16 }}>
          <h3>Container Logs</h3>
          <div style={{ marginBottom: 8, fontSize: 12, color: "#666" }}>
            Deployment: {selectedDeploymentId.slice(0, 8)}...
          </div>
          <LogViewer deploymentId={selectedDeploymentId} token={token} />
        </section>
      )}

      <section style={{ marginTop: 14, fontSize: 12, color: "#666" }}>
        <div><b>Note:</b> Docker services need a Dockerfile. Node.js services auto-detect from package.json.</div>
        <div>MongoDB and Redis are managed services - no repo needed.</div>
      </section>
    </div>
  );
}
