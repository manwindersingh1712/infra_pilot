import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
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
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
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
      style: { stroke: "#9ca3af" }
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
        style: { stroke: "#9ca3af" }
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
      <div style={{ display: "flex", justifyContent: "center", alignItems: "center", height: "100vh" }}>
        <div>Loading canvas...</div>
      </div>
    );
  }

  if (error) {
    return (
      <div style={{ padding: "40px", textAlign: "center" }}>
        <div style={{ color: "#991b1b", marginBottom: "16px" }}>Error: {error}</div>
        <button onClick={loadCanvasData}>Retry</button>
      </div>
    );
  }

  return (
    <div style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
      {/* Header */}
      <header
        style={{
          background: "white",
          borderBottom: "1px solid #e5e7eb",
          padding: "12px 20px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          boxShadow: "0 1px 3px rgba(0,0,0,0.1)"
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
          <button
            onClick={onBack}
            style={{
              padding: "8px 16px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              background: "white",
              cursor: "pointer",
              fontSize: "14px",
              display: "flex",
              alignItems: "center",
              gap: "6px"
            }}
          >
            ← Back to Projects
          </button>
          <h1 style={{ margin: 0, fontSize: "20px", fontWeight: 600 }}>
            {projectName}
          </h1>
        </div>

        <div style={{ display: "flex", gap: "12px" }}>
          <button
            onClick={runAutoLayout}
            style={{
              padding: "8px 16px",
              borderRadius: "6px",
              border: "1px solid #d1d5db",
              background: "white",
              cursor: "pointer",
              fontSize: "14px"
            }}
          >
            Auto Layout
          </button>
          <button
            onClick={() => setIsCreateModalOpen(true)}
            style={{
              padding: "8px 20px",
              borderRadius: "6px",
              border: "none",
              background: "#3b82f6",
              color: "white",
              cursor: "pointer",
              fontSize: "14px",
              fontWeight: 500
            }}
          >
            + Add Service
          </button>
        </div>
      </header>

      {/* Canvas */}
      <div style={{ flex: 1, position: "relative" }}>
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
          <Background gap={20} size={1} color="#e5e7eb" />
          <Controls />
          <MiniMap
            nodeStrokeWidth={3}
            zoomable
            pannable
            style={{
              backgroundColor: "#f9fafb",
              border: "1px solid #e5e7eb"
            }}
          />

          <Panel position="top-left" style={{ marginLeft: "10px", marginTop: "10px" }}>
            <div
              style={{
                background: "white",
                padding: "12px 16px",
                borderRadius: "8px",
                fontSize: "13px",
                color: "#6b7280",
                boxShadow: "0 2px 8px rgba(0,0,0,0.1)",
                border: "1px solid #e5e7eb"
              }}
            >
              <div><strong>Tip:</strong> Drag nodes to reposition</div>
              <div>Drag from handles to connect services</div>
              <div>Click a service to view details</div>
            </div>
          </Panel>

          <Panel position="bottom-center" style={{ marginBottom: "10px" }}>
            <div
              style={{
                background: "white",
                padding: "8px 16px",
                borderRadius: "20px",
                fontSize: "13px",
                color: "#6b7280",
                boxShadow: "0 2px 8px rgba(0,0,0,0.1)",
                border: "1px solid #e5e7eb"
              }}
            >
              {services.length} service{services.length !== 1 ? "s" : ""}
              {connections.length > 0 && ` • ${connections.length} connection${connections.length !== 1 ? "s" : ""}`}
            </div>
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
