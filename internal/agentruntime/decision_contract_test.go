package agentruntime

import (
	"fmt"
	"sync"
	"testing"
)

func TestDecisionServiceContractAcrossKindsAndRuns(t *testing.T) {
	var service DecisionService
	requests := []DecisionRequest{
		{ID: "approval-1", RunID: "run-1", SessionID: "session-1", Kind: DecisionApproval},
		{ID: "question-1", RunID: "run-1", SessionID: "session-1", Kind: DecisionQuestion},
		{ID: "approval-2", RunID: "run-2", SessionID: "session-2", Kind: DecisionApproval},
	}
	for _, request := range requests {
		if err := service.Register(request); err != nil {
			t.Fatalf("Register(%#v): %v", request, err)
		}
	}
	if _, err := service.Resolve(DecisionResolution{ID: "approval-1", Kind: DecisionQuestion}); err == nil {
		t.Fatal("Resolve accepted a mismatched decision kind")
	}
	if got := len(service.Pending()); got != len(requests) {
		t.Fatalf("pending after kind mismatch = %d, want %d", got, len(requests))
	}

	cleared := service.ClearRun("run-1")
	if len(cleared) != 2 {
		t.Fatalf("ClearRun(run-1) cleared %d decisions, want 2", len(cleared))
	}
	pending := service.Pending()
	if len(pending) != 1 || pending[0].ID != "approval-2" {
		t.Fatalf("pending after run isolation = %#v", pending)
	}
}

func TestDecisionServiceConcurrentFirstResponseWins(t *testing.T) {
	var service DecisionService
	request := DecisionRequest{ID: "approval-race", RunID: "run-race", Kind: DecisionApproval}
	if err := service.Register(request); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	results := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			_, err := service.Resolve(DecisionResolution{
				ID: request.ID, Kind: DecisionApproval, Status: "resolved", Value: fmt.Sprintf("decision-%d", i),
			})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent resolutions = %d, want 1", successes)
	}
	if pending := service.Pending(); len(pending) != 0 {
		t.Fatalf("pending after concurrent resolution = %#v", pending)
	}
}
