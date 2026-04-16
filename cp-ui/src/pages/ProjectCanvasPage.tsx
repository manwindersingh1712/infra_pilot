import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  useNodesState,
  useEdgesState,
  addEdge,
  type Edge,
  type Node,
  Panel,
  type Connection
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { ServiceNode } from "../components/ServiceNode";
import { CreateServiceModal } from "../components/CreateServiceModal";
import { ServiceDetailsPanel } from "../components/ServiceDetailsPanel";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

const nodeTypes = {
  service: ServiceNode
};

interface Service {
  id: string;
  name: string;
  serviceType: string;
  positionX: number;
  positionY: number;
  deploymentCount: number;
}

interface ConnectionData {
  id: string;
  sourceId: string;
  targetId: string;
  label?: string;
}

interface ProjectCanvasPageProps {
  projectId: string;
  onBack: () => void;
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

export function ProjectCanvasPage({ projectId, onBack }: ProjectCanvasPageProps) {
  const [projectName, setProjectName] = useState("");
  const [services, setServices] = useState<Service[]>([]);
  const [connections, setConnections] = useState<ConnectionData[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [selectedService, setSelectedService] = useState<Service | null>(null);
  const [isPanelOpen, setIsPanelOpen] = useState(false);
  const [showUserDropdown, setShowUserDropdown] = useState(false);

  // Load project and canvas data
  useEffect(() => {
    loadCanvasData();
  }, [projectId]);

  async function loadCanvasData() {
    if (!projectId) return;
    setLoading(true);
    setError(null);

    try {
      // Load project name
      const project = await apiFetch<{ name: string }>(`/projects/${projectId}`);
      setProjectName(project.name);

      // Load canvas data
      const canvas = await apiFetch<{ services: Service[]; connections: ConnectionData[] }>(
        `/projects/${projectId}/canvas`
      );
      setServices(canvas.services);
      setConnections(canvas.connections);

      // Auto-open create modal if no services exist
      if (canvas.services.length === 0) {
        setIsCreateModalOpen(true);
      }
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }

  function handleLogout() {
    localStorage.removeItem("cp_token");
    window.location.href = "/";
  }

  // Convert services to React Flow nodes
  const initialNodes: Node[] = useMemo(() => {
    return services.map(service => ({
      id: service.id,
      type: "service",
      position: { x: service.positionX, y: service.positionY },
      data: {
        name: service.name,
        serviceType: service.serviceType,
        deploymentCount: service.deploymentCount
      }
    }));
  }, [services]);

  // Convert connections to React Flow edges
  const initialEdges: Edge[] = useMemo(() => {
    return connections.map(conn => ({
      id: conn.id,
      source: conn.sourceId,
      target: conn.targetId,
      label: conn.label,
      type: "smoothstep",
      animated: true,
      style: { stroke: "#8b5cf6" }
    }));
  }, [connections]);

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  // Sync when data changes
  useEffect(() => {
    setNodes(initialNodes);
  }, [initialNodes, setNodes]);

  useEffect(() => {
    setEdges(initialEdges);
  }, [initialEdges, setEdges]);

  // Update position when node is dragged
  const onNodeDragStop = useCallback(
    async (_: React.MouseEvent, node: Node) => {
      try {
        await apiFetch(
          `/services/${node.id}/position`,
          {
            method: "POST",
            body: JSON.stringify({ positionX: node.position.x, positionY: node.position.y })
          }
        );
        // Update local state
        setServices(prev =>
          prev.map(s =>
            s.id === node.id ? { ...s, positionX: node.position.x, positionY: node.position.y } : s
          )
        );
      } catch (err) {
        console.error("Failed to update position:", err);
      }
    },
    []
  );

  // Handle node click to open panel
  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    const service = services.find(s => s.id === node.id);
    if (service) {
      setSelectedService(service);
      setIsPanelOpen(true);
    }
  }, [services]);

  // Create connection between services
  const onConnect = useCallback(
    async (connection: Connection) => {
      if (!connection.source || !connection.target) return;

      // Optimistically add edge
      const newEdge: Edge = {
        id: `temp-${Date.now()}`,
        source: connection.source,
        target: connection.target,
        type: "smoothstep",
        animated: true,
        style: { stroke: "#8b5cf6" }
      };
      setEdges(eds => addEdge(newEdge, eds));

      try {
        const saved = await apiFetch<ConnectionData>(
          "/services/connections",
          {
            method: "POST",
            body: JSON.stringify({ sourceId: connection.source, targetId: connection.target })
          }
        );
        setConnections(prev => [...prev, saved]);
        setEdges(eds =>
          eds.map(e =>
            e.id === newEdge.id ? { ...e, id: saved.id, label: saved.label } : e
          )
        );
      } catch (err) {
        setEdges(eds => eds.filter(e => e.id !== newEdge.id));
        console.error("Failed to create connection:", err);
      }
    },
    [setEdges]
  );

  // Delete connection
  const onEdgesDelete = useCallback(
    async (deletedEdges: Edge[]) => {
      for (const edge of deletedEdges) {
        try {
          await apiFetch(`/services/connections/${edge.id}`, { method: "DELETE" });
          setConnections(prev => prev.filter(c => c.id !== edge.id));
        } catch (err) {
          console.error("Failed to delete connection:", err);
        }
      }
    },
    []
  );

  // Auto-layout
  async function runAutoLayout() {
    try {
      const result = await apiFetch<{ services: Array<{ id: string; positionX: number; positionY: number }> }>(
        `/projects/${projectId}/canvas/auto-layout`,
        { method: "POST" }
      );
      setServices(prev => {
        const positionMap = new Map(result.services.map(s => [s.id, s]));
        return prev.map(s => {
          const newPos = positionMap.get(s.id);
          return newPos ? { ...s, positionX: newPos.positionX, positionY: newPos.positionY } : s;
        });
      });
    } catch (err) {
      console.error("Auto-layout failed:", err);
    }
  }

  if (loading) {
    return (
      <div style={{
        display: "flex",
        justifyContent: "center",
        alignItems: "center",
        height: "100vh",
        background: "#0a0a0a",
        color: "#9ca3af",
        fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      }}>
        <div>Loading canvas...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={{
        padding: "40px",
        textAlign: "center",
        background: "#0a0a0a",
        color: "#9ca3af",
        minHeight: "100vh",
        fontFamily: 'system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif',
      }}>
        <div style={{ color: "#ef4444", marginBottom: "16px" }}>Error: {error}</div>
        <button
          onClick={loadCanvasData}
          style={{
            padding: "10px 20px",
            background: "linear-gradient(135deg, #8b5cf6, #7c3aed)",
            color: "white",
            border: "none",
            borderRadius: "8px",
            cursor: "pointer",
            fontSize: "14px",
            fontWeight: 500,
          }}
        >
          Retry
        </button>
      </div>
    );
  }

  return (
    <div style={{
      height: "100vh",
      display: "flex",
      flexDirection: "column",
      background: `
        radial-gradient(ellipse at 20% 20%, rgba(99, 102, 241, 0.15) 0%, transparent 50%),
        radial-gradient(ellipse at 80% 80%, rgba(139, 92, 246, 0.12) 0%, transparent 50%),
        radial-gradient(ellipse at 50% 50%, rgba(124, 58, 237, 0.08) 0%, transparent 70%),
        linear-gradient(135deg, #1a1a2e 0%, #16162a 50%, #0f0f1a 100%)
      `,
      position: "relative",
    }}>
      {/* Dotted Grid Pattern */}
      <div
        style={{
          position: "absolute",
          inset: 0,
          backgroundImage: `radial-gradient(rgba(139, 92, 246, 0.15) 1px, transparent 1px)`,
          backgroundSize: "24px 24px",
          pointerEvents: "none",
          zIndex: 0,
        }}
      />

      {/* Header */}
      <header
        style={{
          background: "rgba(10, 10, 15, 0.7)",
          backdropFilter: "blur(10px)",
          borderBottom: "1px solid rgba(139, 92, 246, 0.15)",
          padding: "16px 48px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          position: "relative",
          zIndex: 10,
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
          <button
            onClick={onBack}
            style={{
              padding: "8px 16px",
              borderRadius: "8px",
              border: "1px solid rgba(255,255,255,0.1)",
              background: "rgba(255,255,255,0.05)",
              color: "#e5e7eb",
              cursor: "pointer",
              fontSize: "14px",
              display: "flex",
              alignItems: "center",
              gap: "6px",
              transition: "background 0.2s",
            }}
            onMouseEnter={(e) =>
              (e.currentTarget.style.background = "rgba(255,255,255,0.1)")
            }
            onMouseLeave={(e) =>
              (e.currentTarget.style.background = "rgba(255,255,255,0.05)")
            }
          >
            ← Back to Projects
          </button>
          <div style={{ width: "1px", height: "24px", background: "rgba(255,255,255,0.1)" }} />
          <h1 style={{ margin: 0, fontSize: "18px", fontWeight: 600, color: "#fff" }}>
            {projectName}
          </h1>
        </div>

        <div style={{ display: "flex", gap: "12px", alignItems: "center" }}>
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
                      (e.currentTarget.style.background = "rgba(255,255,255,0.05)")
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
                      (e.currentTarget.style.background = "rgba(239,68,68,0.1)")
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

      {/* Canvas */}
      <div style={{ flex: 1, position: "relative", zIndex: 1, borderRadius: "16px", overflow: "hidden", margin: "16px" }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeDragStop={onNodeDragStop}
          onConnect={onConnect}
          onEdgesDelete={onEdgesDelete}
          onNodeClick={onNodeClick}
          nodeTypes={nodeTypes}
          fitView
          fitViewOptions={{ padding: 0.2 }}
          attributionPosition="bottom-right"
          deleteKeyCode="Delete"
        >
          <Background
            gap={24}
            size={2}
            color="rgba(139, 92, 246, 0.25)"
            // variant="dots"
          />
          <Controls
            className="dark-controls"
            style={{
              background: "rgba(30, 30, 40, 0.8)",
              border: "1px solid rgba(255,255,255,0.1)",
              borderRadius: "12px",
              overflow: "hidden",
            }}
          />
          <style>{`
            .dark-controls {
              box-shadow: 0 4px 20px rgba(0,0,0,0.3) !important;
            }
            .dark-controls button {
              background: transparent !important;
              border-bottom: 1px solid rgba(255,255,255,0.05) !important;
              color: #9ca3af !important;
              width: 36px !important;
              height: 36px !important;
              display: flex !important;
              align-items: center !important;
              justify-content: center !important;
            }
            .dark-controls button:last-child {
              border-bottom: none !important;
            }
            .dark-controls button svg {
              fill: #9ca3af !important;
              width: 16px !important;
              height: 16px !important;
            }
            .dark-controls button:hover {
              background: rgba(255,255,255,0.05) !important;
              color: #e5e7eb !important;
            }
            .dark-controls button:hover svg {
              fill: #e5e7eb !important;
            }
          `}</style>
          <Panel position="bottom-center" style={{ marginBottom: "10px" }}>
            <div
              style={{
                background: "rgba(255,255,255,0.05)",
                padding: "8px 16px",
                borderRadius: "20px",
                fontSize: "13px",
                color: "#9ca3af",
                border: "1px solid rgba(255,255,255,0.1)",
              }}
            >
              {services.length} service{services.length !== 1 ? "s" : ""}
              {connections.length > 0 && ` • ${connections.length} connection${connections.length !== 1 ? "s" : ""}`}
            </div>
          </Panel>

          <Panel position="top-right" style={{ marginTop: "10px", marginRight: "10px" }}>
            <button
              onClick={() => setIsCreateModalOpen(true)}
              style={{
                padding: "8px 16px",
                borderRadius: "10px",
                border: "1px solid rgba(255,255,255,0.15)",
                background: "rgba(30, 30, 40, 0.8)",
                color: "#e5e7eb",
                cursor: "pointer",
                fontSize: "14px",
                fontWeight: 500,
                transition: "background 0.2s, border-color 0.2s",
                display: "flex",
                alignItems: "center",
                gap: "8px",
                backdropFilter: "blur(8px)",
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = "rgba(40, 40, 50, 0.9)";
                e.currentTarget.style.borderColor = "rgba(255,255,255,0.25)";
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = "rgba(30, 30, 40, 0.8)";
                e.currentTarget.style.borderColor = "rgba(255,255,255,0.15)";
              }}
            >
              <span style={{ fontSize: "16px", fontWeight: 300, opacity: 0.8 }}>+</span> Add
            </button>
          </Panel>
        </ReactFlow>
      </div>

      {/* Create Service Modal */}
      <CreateServiceModal
        projectId={projectId}
        isOpen={isCreateModalOpen}
        onClose={() => setIsCreateModalOpen(false)}
        onCreated={loadCanvasData}
      />

      {/* Service Details Panel */}
      <ServiceDetailsPanel
        service={selectedService}
        isOpen={isPanelOpen}
        onClose={() => {
          setIsPanelOpen(false);
          setSelectedService(null);
        }}
      />
    </div>
  );
}
