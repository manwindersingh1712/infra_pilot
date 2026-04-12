import { useCallback, useMemo, useState, useEffect } from "react";
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
      style: { stroke: "#9ca3af" }
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
        style: { stroke: "#9ca3af" }
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
          background: "#fee2e2",
          color: "#991b1b",
          borderRadius: "8px"
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
        border: "1px solid #e5e7eb",
        borderRadius: "8px",
        overflow: "hidden",
        background: "#f3f4f6"
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

          <Panel position="top-right" style={{ marginTop: "10px" }}>
            <div
              style={{
                background: "white",
                padding: "8px 12px",
                borderRadius: "6px",
                fontSize: "12px",
                color: "#6b7280",
                boxShadow: "0 1px 3px rgba(0,0,0,0.1)"
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
              background: "rgba(255,255,255,0.9)",
              padding: "16px 24px",
              borderRadius: "8px",
              boxShadow: "0 4px 12px rgba(0,0,0,0.15)"
            }}
          >
            Loading canvas...
          </div>
        )}
      </div>
    </div>
  );
}
