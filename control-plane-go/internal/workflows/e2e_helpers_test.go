package workflows_test

import (
	"testing"

	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

type countReq = workflowservice.CountWorkflowExecutionsRequest

func historyFrom(t *testing.T, iter client.HistoryEventIterator) *historypb.History {
	t.Helper()
	h := &historypb.History{}
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		h.Events = append(h.Events, ev)
	}
	return h
}
