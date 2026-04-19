interface Deployment {
  id: string;
  status: string;
  commitSha: string;
  createdAt: string;
  runtimeUrl?: string | null;
}

interface DeploymentStageTrackerProps {
  deployment: Deployment;
  isExpanded: boolean;
  onToggle: () => void;
  onViewLogs: (deploymentId: string) => void;
}

type StageStatus = "pending" | "running" | "completed";

interface Stage {
  name: string;
  subtext?: string;
}

const STAGES: Stage[] = [
  { name: "Initialization" },
  { name: "Build", subtext: "Building the image..." },
  { name: "Deploy" },
  { name: "Post-deploy" },
];

function getStageStatus(stageIndex: number, deploymentStatus: string): StageStatus {
  if (deploymentStatus === "deployed") return "completed";

  const statusOrder = ["queued", "building", "deploying"];
  const currentIndex = statusOrder.indexOf(deploymentStatus);

  if (currentIndex === -1) {
    if (deploymentStatus === "failed") {
      return stageIndex <= 1 ? "completed" : "pending";
    }
    return "pending";
  }

  if (stageIndex < currentIndex) return "completed";
  if (stageIndex === currentIndex) return "running";
  return "pending";
}

function Spinner() {
  return (
    <span
      style={{
        display: "inline-block",
        width: "16px",
        height: "16px",
        border: "2px solid rgba(59, 130, 246, 0.3)",
        borderTopColor: "#3b82f6",
        borderRadius: "50%",
        animation: "spin 1s linear infinite",
      }}
    />
  );
}

function Checkmark() {
  return (
    <span
      style={{
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        width: "16px",
        height: "16px",
        color: "#22c55e",
        fontSize: "14px",
      }}
    >
      ✓
    </span>
  );
}

function EmptyCircle() {
  return (
    <span
      style={{
        display: "inline-block",
        width: "16px",
        height: "16px",
        border: "2px solid rgba(75, 85, 99, 0.5)",
        borderRadius: "50%",
      }}
    />
  );
}

export function DeploymentStageTracker({ deployment, isExpanded, onToggle, onViewLogs }: DeploymentStageTrackerProps) {
  const isPending = deployment.status === "queued" || deployment.status === "building" || deployment.status === "deploying";
  const isSuccessful = deployment.status === "deployed";
  const isFailed = deployment.status === "failed";

  const getStatusColor = () => {
    if (isSuccessful) return { bg: "rgba(34, 197, 94, 0.1)", text: "#22c55e", border: "rgba(34, 197, 94, 0.2)" };
    if (isFailed) return { bg: "rgba(239, 68, 68, 0.1)", text: "#ef4444", border: "rgba(239, 68, 68, 0.2)" };
    if (isPending) return { bg: "rgba(59, 130, 246, 0.1)", text: "#3b82f6", border: "rgba(59, 130, 246, 0.2)" };
    return { bg: "rgba(107, 114, 128, 0.1)", text: "#9ca3af", border: "rgba(107, 114, 128, 0.2)" };
  };

  const statusColor = getStatusColor();

  return (
    <div
      style={{
        borderRadius: "8px",
        border: "1px solid rgba(255, 255, 255, 0.1)",
        background: "rgba(255, 255, 255, 0.03)",
        overflow: "hidden",
      }}
    >
      {/* Header */}
      <div
        style={{
          padding: "16px",
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          cursor: "pointer",
        }}
        onClick={onToggle}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
          {/* Status Badge */}
          <span
            style={{
              fontSize: "12px",
              padding: "4px 10px",
              borderRadius: "4px",
              background: statusColor.bg,
              color: statusColor.text,
              fontWeight: 600,
              textTransform: "uppercase",
              border: `1px solid ${statusColor.border}`,
              display: "flex",
              alignItems: "center",
              gap: "6px",
            }}
          >
            {isPending && <Spinner />}
            {deployment.status}
          </span>

          {/* Commit Info */}
          <div>
            <div style={{ fontSize: "14px", color: "#e5e7eb", fontWeight: 500 }}>
              {deployment.commitSha.slice(0, 8)}
            </div>
            <div style={{ fontSize: "12px", color: "#6b7280" }}>
              {new Date(deployment.createdAt).toLocaleString()}
            </div>
            {deployment.runtimeUrl && (
              <a
                href={deployment.runtimeUrl}
                target="_blank"
                rel="noopener noreferrer"
                onClick={(e) => e.stopPropagation()}
                style={{
                  fontSize: "12px",
                  color: "#8b5cf6",
                  textDecoration: "none",
                  fontWeight: 500,
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.textDecoration = "underline";
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.textDecoration = "none";
                }}
              >
                {deployment.runtimeUrl}
              </a>
            )}
          </div>
        </div>

        <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              onViewLogs(deployment.id);
            }}
            style={{
              padding: "6px 12px",
              borderRadius: "6px",
              border: "1px solid rgba(255, 255, 255, 0.1)",
              background: "rgba(255, 255, 255, 0.05)",
              color: "#9ca3af",
              fontSize: "13px",
              cursor: "pointer",
              transition: "background 0.2s, color 0.2s",
            }}
            onMouseEnter={(e) => {
              e.currentTarget.style.background = "rgba(255, 255, 255, 0.1)";
              e.currentTarget.style.color = "#e5e7eb";
            }}
            onMouseLeave={(e) => {
              e.currentTarget.style.background = "rgba(255, 255, 255, 0.05)";
              e.currentTarget.style.color = "#9ca3af";
            }}
          >
            View logs
          </button>

          {/* Expand/Collapse Chevron */}
          <span
            style={{
              color: "#6b7280",
              fontSize: "12px",
              transform: isExpanded ? "rotate(180deg)" : "rotate(0deg)",
              transition: "transform 0.2s",
            }}
          >
            ▼
          </span>
        </div>
      </div>

      {/* Expanded Stage List */}
      {isExpanded && (
        <div
          style={{
            borderTop: "1px solid rgba(255, 255, 255, 0.05)",
            background: "rgba(0, 0, 0, 0.2)",
          }}
        >
          {/* Summary Header */}
          <div
            style={{
              padding: "12px 16px",
              display: "flex",
              alignItems: "center",
              gap: "8px",
              color: isSuccessful ? "#22c55e" : isPending ? "#3b82f6" : "#9ca3af",
              fontSize: "14px",
              borderBottom: "1px solid rgba(255, 255, 255, 0.05)",
            }}
          >
            {isSuccessful && <Checkmark />}
            {isFailed && (
              <span style={{ color: "#ef4444" }}>✕</span>
            )}
            {isPending && <Spinner />}
            <span>
              {isSuccessful
                ? "Deployment successful"
                : isFailed
                ? "Deployment failed"
                : "Deployment in progress:"}
            </span>
          </div>

          {/* Stages */}
          <div style={{ padding: "16px" }}>
            {STAGES.map((stage, index) => {
              const status = getStageStatus(index, deployment.status);

              return (
                <div
                  key={stage.name}
                  style={{
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "space-between",
                    padding: "10px 0",
                    borderBottom:
                      index < STAGES.length - 1
                        ? "1px solid rgba(255, 255, 255, 0.05)"
                        : "none",
                  }}
                >
                  <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
                    {status === "completed" && <Checkmark />}
                    {status === "running" && <Spinner />}
                    {status === "pending" && <EmptyCircle />}

                    <div>
                      <div
                        style={{
                          fontSize: "14px",
                          color:
                            status === "completed"
                              ? "#e5e7eb"
                              : status === "running"
                              ? "#e5e7eb"
                              : "#6b7280",
                          fontWeight: status === "running" ? 500 : 400,
                        }}
                      >
                        {stage.name}
                      </div>
                      {status === "running" && stage.subtext && (
                        <div style={{ fontSize: "12px", color: "#6b7280", marginTop: "2px" }}>
                          {stage.subtext}
                        </div>
                      )}
                    </div>
                  </div>

                  <div style={{ fontSize: "13px", color: "#6b7280" }}>
                    {status === "pending" && "Not started"}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}
