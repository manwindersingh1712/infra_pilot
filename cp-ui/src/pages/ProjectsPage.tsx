import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

interface Project {
  id: string;
  name: string;
  createdAt: string;
  _count?: {
    services: number;
  };
}

function getToken() {
  return localStorage.getItem("cp_token") ?? "";
}

async function apiFetch<T>(
  path: string,
  opts: RequestInit = {}
): Promise<T> {
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

export function ProjectsPage() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [newProjectName, setNewProjectName] = useState("");
  const [isCreating, setIsCreating] = useState(false);

  useEffect(() => {
    loadProjects();
  }, []);

  async function loadProjects() {
    setLoading(true);
    try {
      const data = await apiFetch<Project[]>("/projects");
      setProjects(data);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  async function createProject() {
    if (!newProjectName.trim()) return;
    setIsCreating(true);
    try {
      await apiFetch("/projects", {
        method: "POST",
        body: JSON.stringify({ name: newProjectName }),
      });
      setNewProjectName("");
      await loadProjects();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setIsCreating(false);
    }
  }

  const filteredProjects = projects.filter(p =>
    p.name.toLowerCase().includes(search.toLowerCase())
  );

  if (loading) {
    return (
      <div style={{
        display: "flex",
        justifyContent: "center",
        alignItems: "center",
        height: "100vh",
        fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif",
        background: "#f3f4f6",
        color: "#1f2937"
      }}>
        <div style={{ fontSize: "16px" }}>Loading projects...</div>
      </div>
    );
  }

  return (
    <div style={{
      minHeight: "100vh",
      width: "100%",
      background: "#f3f4f6",
      fontFamily: "system-ui, -apple-system, Segoe UI, Roboto, Arial, sans-serif",
      color: "#1f2937"
    }}>
      {/* Header */}
      <header style={{
        background: "white",
        borderBottom: "1px solid #e5e7eb",
        padding: "16px 24px",
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between"
      }}>
        <h1 style={{ margin: 0, fontSize: "24px", fontWeight: 600, color: "#111827" }}>Infra Pilot</h1>
        <div style={{ display: "flex", gap: "12px", alignItems: "center" }}>
          <input
            type="text"
            placeholder="Search projects..."
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            style={{
              padding: "8px 12px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              width: "250px",
              fontSize: "14px",
              outline: "none"
            }}
          />
        </div>
      </header>

      {/* Main Content */}
      <main style={{
        padding: "24px",
        maxWidth: "1200px",
        margin: "0 auto"
      }}>
        {/* Create Project */}
        <div style={{
          background: "white",
          borderRadius: "8px",
          padding: "16px",
          marginBottom: "24px",
          display: "flex",
          gap: "12px",
          alignItems: "center",
          boxShadow: "0 1px 3px rgba(0,0,0,0.1)"
        }}>
          <input
            type="text"
            placeholder="New project name..."
            value={newProjectName}
            onChange={(e) => setNewProjectName(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && createProject()}
            style={{
              flex: 1,
              padding: "10px 12px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              fontSize: "14px",
              outline: "none"
            }}
          />
          <button
            onClick={createProject}
            disabled={isCreating || !newProjectName.trim()}
            style={{
              padding: "10px 20px",
              background: "#3b82f6",
              color: "white",
              border: "none",
              borderRadius: "6px",
              cursor: isCreating ? "not-allowed" : "pointer",
              opacity: isCreating ? 0.6 : 1,
              fontWeight: 500,
              fontSize: "14px"
            }}
          >
            {isCreating ? "Creating..." : "Create Project"}
          </button>
        </div>

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

        {/* Projects Grid */}
        <div style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fill, minmax(300px, 1fr))",
          gap: "16px"
        }}>
          {filteredProjects.map((project) => (
            <div
              key={project.id}
              onClick={() => navigate(`/projects/${project.id}`)}
              style={{
                background: "white",
                borderRadius: "8px",
                padding: "20px",
                cursor: "pointer",
                border: "1px solid #e5e7eb",
                transition: "box-shadow 0.2s, transform 0.2s",
                boxShadow: "0 1px 3px rgba(0,0,0,0.1)"
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.boxShadow = "0 4px 12px rgba(0,0,0,0.15)";
                e.currentTarget.style.transform = "translateY(-2px)";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.boxShadow = "0 1px 3px rgba(0,0,0,0.1)";
                e.currentTarget.style.transform = "translateY(0)";
              }}
            >
              <div style={{ display: "flex", alignItems: "center", gap: "12px", marginBottom: "12px" }}>
                <div style={{
                  width: "48px",
                  height: "48px",
                  background: "linear-gradient(135deg, #3b82f6, #1d4ed8)",
                  borderRadius: "10px",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  color: "white",
                  fontSize: "24px"
                }}>
                  📁
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <h3 style={{
                    margin: 0,
                    fontSize: "16px",
                    fontWeight: 600,
                    color: "#111827",
                    whiteSpace: "nowrap",
                    overflow: "hidden",
                    textOverflow: "ellipsis"
                  }}>{project.name}</h3>
                  <p style={{ margin: "4px 0 0 0", fontSize: "13px", color: "#6b7280" }}>
                    {project._count?.services ?? 0} services
                  </p>
                </div>
              </div>
              <p style={{ margin: 0, fontSize: "12px", color: "#9ca3af" }}>
                Created {new Date(project.createdAt).toLocaleDateString()}
              </p>
            </div>
          ))}
        </div>

        {filteredProjects.length === 0 && !loading && (
          <div style={{
            textAlign: "center",
            padding: "48px",
            color: "#6b7280"
          }}>
            <p>No projects found.</p>
            <p>Create your first project to get started!</p>
          </div>
        )}
      </main>
    </div>
  );
}
