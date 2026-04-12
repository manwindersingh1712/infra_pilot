import { Handle, Position } from "@xyflow/react";

const serviceIcons: Record<string, string> = {
  docker: "🐳",
  nodejs: "🟢",
  nextjs: "▲",
  react: "⚛️",
  mongodb: "🍃",
  redis: "🔴"
};

const serviceColors: Record<string, string> = {
  docker: "#2496ed",
  nodejs: "#339933",
  nextjs: "#fff",
  react: "#61dafb",
  mongodb: "#47a248",
  redis: "#dc382d"
};

interface ServiceNodeData {
  name: string;
  serviceType: string;
  deploymentCount: number;
  runtimeUrl?: string;
}

interface ServiceNodeProps {
  id: string;
  data: ServiceNodeData;
  selected: boolean;
}

export function ServiceNode({ id, data, selected }: ServiceNodeProps) {
  const icon = serviceIcons[data.serviceType] || "📦";
  const color = serviceColors[data.serviceType] || "#8b5cf6";
  const hasDeployments = data.deploymentCount > 0;

  return (
    <div
      style={{
        padding: "12px 16px",
        borderRadius: "10px",
        background: "rgba(255,255,255,0.03)",
        border: `2px solid ${selected ? "#8b5cf6" : color}`,
        boxShadow: selected
          ? "0 0 0 3px rgba(139, 92, 246, 0.3), 0 4px 12px rgba(0,0,0,0.3)"
          : "0 2px 8px rgba(0,0,0,0.3)",
        minWidth: "160px",
        transition: "box-shadow 0.15s ease, transform 0.15s ease",
        cursor: "grab",
        backdropFilter: "blur(8px)",
      }}
    >
      {/* Input handle (left side) */}
      <Handle
        type="target"
        position={Position.Left}
        style={{
          width: "10px",
          height: "10px",
          background: color,
          border: "2px solid #0a0a0a",
        }}
      />

      {/* Content */}
      <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
        <span style={{ fontSize: "24px" }}>{icon}</span>
        <div style={{ minWidth: 0 }}>
          <div
            style={{
              fontWeight: 600,
              fontSize: "14px",
              color: "#fff",
              whiteSpace: "nowrap",
              overflow: "hidden",
              textOverflow: "ellipsis",
            }}
            title={data.name}
          >
            {data.name}
          </div>
          <div
            style={{
              fontSize: "11px",
              color: "#9ca3af",
              textTransform: "capitalize",
              display: "flex",
              alignItems: "center",
              gap: "6px",
            }}
          >
            {data.serviceType}
            {hasDeployments && (
              <span
                style={{
                  width: "6px",
                  height: "6px",
                  borderRadius: "50%",
                  background: "#22c55e",
                }}
                title={`${data.deploymentCount} deployment(s)`}
              />
            )}
          </div>
        </div>
      </div>

      {/* Output handle (right side) */}
      <Handle
        type="source"
        position={Position.Right}
        style={{
          width: "10px",
          height: "10px",
          background: color,
          border: "2px solid #0a0a0a",
        }}
      />
    </div>
  );
}
