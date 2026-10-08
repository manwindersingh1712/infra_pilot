package store

// Outbox event types. The Node API writes the first two; the Go worker starts
// DeployWorkflow for both (the workflow decides whether to build).
const (
	EventBuildRequested  = "build.requested"
	EventDeployRequested = "deploy.requested"
)
