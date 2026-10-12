import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import logoSvg from "../assets/logo.svg";
import { apiFetch, humanizeApiError, logout } from "../lib/api";
import { isValidProjectName, PROJECT_NAME_MAX } from "../lib/projectName";

interface Project {
  id: string;
  name: string;
  createdAt: string;
}

export function ProjectsPage() {
  const navigate = useNavigate();
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [newProjectName, setNewProjectName] = useState("");
  const [isCreating, setIsCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showUserDropdown, setShowUserDropdown] = useState(false);

  useEffect(() => {
    loadProjects();
  }, []);

  async function loadProjects() {
    setLoading(true);
    try {
      const data = await apiFetch<Project[]>("/projects");
      setProjects(data);
    } catch (err) {
      setError(humanizeApiError(err));
    } finally {
      setLoading(false);
    }
  }

  async function createProject() {
    if (isCreating || !isValidProjectName(newProjectName)) return;
    setIsCreating(true);
    setCreateError(null);
    try {
      await apiFetch("/projects", {
        method: "POST",
        body: JSON.stringify({ name: newProjectName.trim() }),
      });
      setNewProjectName("");
      setShowCreateModal(false);
      await loadProjects();
    } catch (err) {
      // Shown inside the dialog so the user keeps what they typed.
      setCreateError(
        humanizeApiError(err, {
          invalid_request: "Project name must be 2-100 characters.",
        })
      );
    } finally {
      setIsCreating(false);
    }
  }

  function openCreateModal() {
    setCreateError(null);
    setShowCreateModal(true);
  }

  function handleLogout() {
    void logout().finally(() => navigate("/"));
  }

  if (loading) {
    return (
      <div
        style={{
          display: "flex",
          justifyContent: "center",
          alignItems: "center",
          height: "100vh",
          fontFamily:
            'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
          background: "#0a0a0a",
          color: "#9ca3af",
        }}
      >
        <div style={{ fontSize: "16px" }}>Loading projects...</div>
      </div>
    );
  }

  return (
    <div
      style={{
        minHeight: "100vh",
        width: "100%",
        background: "#0a0a0a",
        fontFamily:
          'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
        color: "#fff",
      }}
    >
      {/* Header */}
      <header
        style={{
          background: "rgba(255,255,255,0.02)",
          borderBottom: "1px solid rgba(255,255,255,0.1)",
          padding: "16px 48px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "32px" }}>
          {/* Logo */}
          <div
            style={{ display: "flex", alignItems: "center", gap: "10px", cursor: "pointer" }}
            onClick={() => navigate("/")}
          >
            <img
              src={logoSvg}
              alt="Infra Pilot"
              style={{
                width: "28px",
                height: "28px",
              }}
            />
            <span style={{ fontSize: "18px", fontWeight: 600 }}>Infra Pilot</span>
          </div>

          {/* Nav */}
          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "8px",
              padding: "8px 16px",
              background: "rgba(255,255,255,0.1)",
              borderRadius: "8px",
              fontSize: "14px",
              color: "#fff",
            }}
          >
            <span>⊞</span>
            <span>Projects</span>
          </div>
        </div>

        <div style={{ display: "flex", gap: "12px", alignItems: "center" }}>
          <button
            onClick={openCreateModal}
            style={{
              padding: "8px 16px",
              background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
              color: "white",
              border: "none",
              borderRadius: "8px",
              cursor: "pointer",
              fontWeight: 500,
              fontSize: "14px",
              display: "flex",
              alignItems: "center",
              gap: "6px",
              transition: "box-shadow 0.2s",
            }}
            onMouseEnter={(e) =>
              (e.currentTarget.style.boxShadow =
                "0 4px 20px rgba(139, 92, 246, 0.4)")
            }
            onMouseLeave={(e) =>
              (e.currentTarget.style.boxShadow = "none")
            }
          >
            <span>+</span> New Project
          </button>

          {/* User Avatar Dropdown */}
          <div style={{ position: "relative" }}>
            <div
              style={{
                display: "flex",
                alignItems: "center",
                gap: "8px",
                cursor: "pointer",
                padding: "4px",
                borderRadius: "8px",
                transition: "background 0.2s",
              }}
              onClick={() => setShowUserDropdown(!showUserDropdown)}
              onMouseEnter={(e) =>
                (e.currentTarget.style.background = "rgba(255,255,255,0.05)")
              }
              onMouseLeave={(e) =>
                (e.currentTarget.style.background = "transparent")
              }
            >
              <div
                style={{
                  width: "32px",
                  height: "32px",
                  borderRadius: "50%",
                  background: "linear-gradient(135deg, #8b5cf6, #6366f1)",
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "center",
                  fontSize: "12px",
                  fontWeight: 600,
                }}
              >
                U
              </div>
              <span style={{ fontSize: "12px", color: "#9ca3af" }}>▼</span>
            </div>

            {/* Dropdown Menu */}
            {showUserDropdown && (
              <>
                <div
                  style={{
                    position: "fixed",
                    inset: 0,
                    zIndex: 99,
                  }}
                  onClick={() => setShowUserDropdown(false)}
                />
                <div
                  style={{
                    position: "absolute",
                    top: "calc(100% + 8px)",
                    right: 0,
                    width: "240px",
                    background: "#1a1a1a",
                    border: "1px solid rgba(255,255,255,0.1)",
                    borderRadius: "12px",
                    padding: "8px",
                    zIndex: 100,
                    boxShadow: "0 10px 40px rgba(0,0,0,0.5)",
                  }}
                >
                  {/* User Info */}
                  <div
                    style={{
                      padding: "12px",
                      borderBottom: "1px solid rgba(255,255,255,0.1)",
                      marginBottom: "4px",
                    }}
                  >
                    <div
                      style={{
                        fontSize: "14px",
                        fontWeight: 500,
                        color: "#fff",
                        whiteSpace: "nowrap",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                      }}
                    >
                      user@example.com
                    </div>
                    <div
                      style={{
                        fontSize: "12px",
                        color: "#6b7280",
                        marginTop: "2px",
                      }}
                    >
                      Free Plan
                    </div>
                  </div>

                  {/* Settings */}
                  <button
                    style={{
                      width: "100%",
                      padding: "10px 12px",
                      background: "transparent",
                      border: "none",
                      borderRadius: "8px",
                      color: "#e5e7eb",
                      fontSize: "14px",
                      textAlign: "left",
                      cursor: "pointer",
                      display: "flex",
                      alignItems: "center",
                      gap: "10px",
                      transition: "background 0.2s",
                    }}
                    onMouseEnter={(e) =>
                      (e.currentTarget.style.background =
                        "rgba(255,255,255,0.05)")
                    }
                    onMouseLeave={(e) =>
                      (e.currentTarget.style.background = "transparent")
                    }
                  >
                    <span>⚙️</span> Settings
                  </button>

                  {/* Sign Out */}
                  <button
                    onClick={handleLogout}
                    style={{
                      width: "100%",
                      padding: "10px 12px",
                      background: "transparent",
                      border: "none",
                      borderRadius: "8px",
                      color: "#f87171",
                      fontSize: "14px",
                      textAlign: "left",
                      cursor: "pointer",
                      display: "flex",
                      alignItems: "center",
                      gap: "10px",
                      transition: "background 0.2s",
                    }}
                    onMouseEnter={(e) =>
                      (e.currentTarget.style.background =
                        "rgba(239,68,68,0.1)")
                    }
                    onMouseLeave={(e) =>
                      (e.currentTarget.style.background = "transparent")
                    }
                  >
                    <span>→</span> Sign out
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      </header>

      {/* Main Content */}
      <main style={{ padding: "48px" }}>
        {/* Title */}
        <div style={{ marginBottom: "48px" }}>
          <h1 style={{ margin: "0 0 8px 0", fontSize: "32px", fontWeight: 600 }}>
            Projects
          </h1>
          <p style={{ margin: 0, fontSize: "16px", color: "#9ca3af" }}>
            Manage and deploy your applications
          </p>
        </div>

        {error && (
          <div
            style={{
              background: "rgba(239,68,68,0.1)",
              color: "#ef4444",
              padding: "12px 16px",
              borderRadius: "8px",
              marginBottom: "24px",
              fontSize: "14px",
              border: "1px solid rgba(239,68,68,0.2)",
            }}
          >
            {error}
          </div>
        )}

        {/* Projects Grid */}
        {projects.length > 0 && (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fill, minmax(300px, 1fr))",
              gap: "16px",
            }}
          >
            {projects.map((project) => (
              <div
                key={project.id}
                onClick={() => navigate(`/projects/${project.id}`)}
                style={{
                  background: "rgba(255,255,255,0.03)",
                  borderRadius: "12px",
                  padding: "20px",
                  cursor: "pointer",
                  border: "1px solid rgba(255,255,255,0.1)",
                  transition: "border-color 0.2s, transform 0.2s",
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.borderColor = "rgba(139,92,246,0.4)";
                  e.currentTarget.style.transform = "translateY(-2px)";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.borderColor = "rgba(255,255,255,0.1)";
                  e.currentTarget.style.transform = "translateY(0)";
                }}
              >
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: "12px",
                    marginBottom: "12px",
                  }}
                >
                  <div
                    style={{
                      width: "48px",
                      height: "48px",
                      background: "linear-gradient(135deg, #8b5cf6, #6366f1)",
                      borderRadius: "10px",
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "center",
                      color: "white",
                      fontSize: "24px",
                    }}
                  >
                    📁
                  </div>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <h3
                      style={{
                        margin: 0,
                        fontSize: "16px",
                        fontWeight: 600,
                        color: "#fff",
                        whiteSpace: "nowrap",
                        overflow: "hidden",
                        textOverflow: "ellipsis",
                      }}
                    >
                      {project.name}
                    </h3>
                  </div>
                </div>
                <p style={{ margin: 0, fontSize: "12px", color: "#6b7280" }}>
                  Created {new Date(project.createdAt).toLocaleDateString()}
                </p>
              </div>
            ))}
          </div>
        )}

        {/* Empty State */}
        {projects.length === 0 && !loading && (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              justifyContent: "center",
              padding: "80px 24px",
              textAlign: "center",
            }}
          >
            {/* Folder Icon */}
            <div
              style={{
                width: "64px",
                height: "64px",
                background: "rgba(255,255,255,0.05)",
                border: "1px solid rgba(255,255,255,0.1)",
                borderRadius: "16px",
                display: "flex",
                alignItems: "center",
                justifyContent: "center",
                fontSize: "28px",
                marginBottom: "24px",
              }}
            >
              📁
            </div>

            <h2
              style={{
                margin: "0 0 8px 0",
                fontSize: "20px",
                fontWeight: 600,
                color: "#fff",
              }}
            >
              No projects yet
            </h2>

            <p
              style={{
                margin: "0 0 24px 0",
                fontSize: "14px",
                color: "#9ca3af",
                maxWidth: "400px",
                lineHeight: 1.6,
              }}
            >
              Create your first project to start deploying applications from
              GitHub.
            </p>

            <button
              onClick={openCreateModal}
              style={{
                padding: "12px 24px",
                background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
                color: "white",
                border: "none",
                borderRadius: "8px",
                cursor: "pointer",
                fontWeight: 500,
                fontSize: "14px",
                display: "flex",
                alignItems: "center",
                gap: "8px",
                transition: "box-shadow 0.2s",
              }}
              onMouseEnter={(e) =>
                (e.currentTarget.style.boxShadow =
                  "0 4px 24px rgba(139, 92, 246, 0.5)")
              }
              onMouseLeave={(e) =>
                (e.currentTarget.style.boxShadow = "none")
              }
            >
              <span>🚀</span> Create Your First Project
            </button>
          </div>
        )}
      </main>

      {/* Create Project Modal */}
      {showCreateModal && (
        <div
          style={{
            position: "fixed",
            inset: 0,
            background: "rgba(0,0,0,0.7)",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            zIndex: 100,
          }}
          onClick={(e) => {
            if (e.target === e.currentTarget) setShowCreateModal(false);
          }}
        >
          <div
            style={{
              background: "#111111",
              border: "1px solid rgba(255,255,255,0.1)",
              borderRadius: "16px",
              padding: "32px",
              width: "100%",
              maxWidth: "400px",
            }}
          >
            <h2
              style={{
                margin: "0 0 8px 0",
                fontSize: "20px",
                fontWeight: 600,
                color: "#fff",
              }}
            >
              Create New Project
            </h2>
            <p
              style={{
                margin: "0 0 24px 0",
                fontSize: "14px",
                color: "#9ca3af",
              }}
            >
              Enter a name for your new project
            </p>

            <input
              type="text"
              placeholder="Project name..."
              value={newProjectName}
              onChange={(e) => {
                setNewProjectName(e.target.value);
                setCreateError(null);
              }}
              onKeyDown={(e) => e.key === "Enter" && createProject()}
              maxLength={PROJECT_NAME_MAX}
              autoFocus
              style={{
                width: "100%",
                padding: "12px 16px",
                background: "rgba(255,255,255,0.05)",
                border: "1px solid rgba(255,255,255,0.1)",
                borderRadius: "8px",
                fontSize: "14px",
                color: "#fff",
                outline: "none",
                marginBottom: "8px",
                boxSizing: "border-box",
              }}
            />
            <div
              style={{
                fontSize: "12px",
                color: "#6b7280",
                marginBottom: createError ? "12px" : "24px",
              }}
            >
              2-100 characters
            </div>
            {createError && (
              <div
                role="alert"
                style={{
                  background: "rgba(239,68,68,0.1)",
                  color: "#ef4444",
                  padding: "10px 12px",
                  borderRadius: "8px",
                  marginBottom: "24px",
                  fontSize: "13px",
                  border: "1px solid rgba(239,68,68,0.2)",
                }}
              >
                {createError}
              </div>
            )}

            <div style={{ display: "flex", gap: "12px" }}>
              <button
                onClick={() => setShowCreateModal(false)}
                style={{
                  flex: 1,
                  padding: "12px",
                  background: "rgba(255,255,255,0.05)",
                  color: "#9ca3af",
                  border: "1px solid rgba(255,255,255,0.1)",
                  borderRadius: "8px",
                  cursor: "pointer",
                  fontSize: "14px",
                }}
              >
                Cancel
              </button>
              <button
                onClick={createProject}
                disabled={isCreating || !isValidProjectName(newProjectName)}
                style={{
                  flex: 1,
                  padding: "12px",
                  background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
                  color: "white",
                  border: "none",
                  borderRadius: "8px",
                  cursor:
                    isCreating || !isValidProjectName(newProjectName)
                      ? "not-allowed"
                      : "pointer",
                  opacity: isCreating || !isValidProjectName(newProjectName) ? 0.6 : 1,
                  fontWeight: 500,
                  fontSize: "14px",
                }}
              >
                {isCreating ? "Creating..." : "Create"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
