import { useCallback, useMemo, useState, useEffect } from "react";
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

import { useCanvas } from "../hooks/useCanvas";
import { ServiceNode } from "./ServiceNode";
import { CanvasToolbar } from "./CanvasToolbar";

const nodeTypes = {
  service: ServiceNode
};

interface ServiceCanvasProps {
  projectId: string;
}

export function ServiceCanvas({ projectId }: ServiceCanvasProps) {
  const { state, loading, error, refresh, updatePosition, createConnection, deleteConnection, runAutoLayout } =
    useCanvas({ projectId });

  const [selectedNode, setSelectedNode] = useState<string | null>(null);

  // Convert services to React Flow nodes
  const initialNodes: Node[] = useMemo(() => {
    if (!state) return [];
    return state.services.map(service => ({
      id: service.id,
      type: "service",
      position: { x: service.positionX, y: service.positionY },
      data: {
        name: service.name,
        serviceType: service.serviceType,
        deploymentCount: service.deploymentCount
      },
      selected: service.id === selectedNode
    }));
  }, [state, selectedNode]);

  // Convert connections to React Flow edges
  const initialEdges: Edge[] = useMemo(() => {
    if (!state) return [];
    return state.connections.map(conn => ({
      id: conn.id,
      source: conn.sourceId,
      target: conn.targetId,
      label: conn.label,
      type: "smoothstep",
      animated: true,
      style: { stroke: "#8b5cf6" }
    }));
  }, [state]);

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  // Sync with initialNodes/initialEdges when data changes
  useEffect(() => {
    setNodes(initialNodes);
  }, [initialNodes, setNodes]);

  useEffect(() => {
    setEdges(initialEdges);
  }, [initialEdges, setEdges]);

  // Handle node drag end - save position
  const onNodeDragStop = useCallback(
    (_: React.MouseEvent, node: Node) => {
      updatePosition(node.id, node.position.x, node.position.y);
    },
    [updatePosition]
  );

  // Handle new connections
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

      // Persist to backend
      try {
        const saved = await createConnection(connection.source, connection.target);
        // Replace temp edge with saved one
        setEdges(eds =>
          eds.map(e =>
            e.id === newEdge.id
              ? { ...e, id: saved!.id, label: saved!.label }
              : e
          )
        );
      } catch (err) {
        // Remove temp edge on error
        setEdges(eds => eds.filter(e => e.id !== newEdge.id));
        console.error("Failed to create connection:", err);
      }
    },
    [createConnection, setEdges]
  );

  // Handle edge deletion (right-click or delete key)
  const onEdgesDelete = useCallback(
    async (deletedEdges: Edge[]) => {
      for (const edge of deletedEdges) {
        await deleteConnection(edge.id);
      }
    },
    [deleteConnection]
  );

  // Handle node selection
  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    setSelectedNode(node.id);
  }, []);

  if (error) {
    return (
      <div
        style={{
          padding: "20px",
          background: "rgba(239,68,68,0.1)",
          color: "#ef4444",
          borderRadius: "8px",
          border: "1px solid rgba(239,68,68,0.2)",
        }}
      >
        <strong>Error loading canvas:</strong> {error}
      </div>
    );
  }

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        height: "600px",
        border: "1px solid rgba(255,255,255,0.1)",
        borderRadius: "8px",
        overflow: "hidden",
        background: "#0a0a0a",
      }}
    >
      <CanvasToolbar
        onAutoLayout={runAutoLayout}
        onRefresh={refresh}
        loading={loading}
        serviceCount={state?.services.length ?? 0}
        connectionCount={state?.connections.length ?? 0}
      />

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
          <Background gap={20} size={1} color="rgba(255,255,255,0.05)" />
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
          <Panel position="top-right" style={{ marginTop: "10px" }}>
            <div
              style={{
                background: "rgba(255,255,255,0.05)",
                padding: "12px 16px",
                borderRadius: "8px",
                fontSize: "13px",
                color: "#9ca3af",
                border: "1px solid rgba(255,255,255,0.1)",
              }}
            >
              <div>Drag nodes to reposition</div>
              <div>Drag from handles to connect</div>
              <div>Select edge + Delete to remove</div>
            </div>
          </Panel>
        </ReactFlow>

        {loading && (
          <div
            style={{
              position: "absolute",
              top: "50%",
              left: "50%",
              transform: "translate(-50%, -50%)",
              background: "rgba(17,17,17,0.95)",
              padding: "16px 24px",
              borderRadius: "8px",
              border: "1px solid rgba(255,255,255,0.1)",
              color: "#e5e7eb",
              boxShadow: "0 4px 12px rgba(0,0,0,0.5)",
            }}
          >
            Loading canvas...
          </div>
        )}
      </div>
    </div>
  );
}
