package codingagent

import (
	"testing"

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
)

func TestSpawnSubagentCarriesProvenance(t *testing.T) {
	t.Parallel()

	agent, root, err := New("You are a coding agent.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	taskNode, err := agent.AddUserTask(root.ID, "Inspect the failing tests.")
	if err != nil {
		t.Fatalf("AddUserTask() error = %v", err)
	}

	subagent, _, subTask, err := agent.SpawnSubagent(taskNode.ID, "You are a search specialist.", "Find references to TestFoo")
	if err != nil {
		t.Fatalf("SpawnSubagent() error = %v", err)
	}

	spawn := subagent.Graph().Session().SpawnedFrom
	if spawn == nil {
		t.Fatalf("subagent session missing spawned_from")
	}
	if spawn.SessionID != agent.Graph().Session().ID {
		t.Fatalf("spawn session = %s, want %s", spawn.SessionID, agent.Graph().Session().ID)
	}
	if spawn.NodeID != taskNode.ID {
		t.Fatalf("spawn node = %s, want %s", spawn.NodeID, taskNode.ID)
	}

	needsResponse, err := subagent.Graph().NeedsResponse(subTask.ID)
	if err != nil {
		t.Fatalf("NeedsResponse() error = %v", err)
	}
	if !needsResponse {
		t.Fatalf("subagent task should require a response")
	}
}

func TestPruneFailedToolCallRewritesCanonicalHistory(t *testing.T) {
	t.Parallel()

	agent, root, err := New("You are a coding agent.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	taskNode, err := agent.AddUserTask(root.ID, "Run the test suite.")
	if err != nil {
		t.Fatalf("AddUserTask() error = %v", err)
	}

	planNode, err := agent.AddAssistantPlan(taskNode.ID, "I will run go test ./... and inspect failures.")
	if err != nil {
		t.Fatalf("AddAssistantPlan() error = %v", err)
	}

	failedToolNode, err := agent.AddToolResult(planNode.ID, "go-test", "exit status 1: network timeout", false)
	if err != nil {
		t.Fatalf("AddToolResult() error = %v", err)
	}

	rewriteNode, err := agent.PruneFailedToolCall(planNode.ID, failedToolNode.ID, "The previous tool call failed transiently and was pruned from the canonical path.")
	if err != nil {
		t.Fatalf("PruneFailedToolCall() error = %v", err)
	}

	lineage, err := agent.Graph().ResumeNodes(rewriteNode.ID)
	if err != nil {
		t.Fatalf("ResumeNodes() error = %v", err)
	}
	if len(lineage) != 4 {
		t.Fatalf("lineage len = %d, want 4", len(lineage))
	}
	if lineage[len(lineage)-1].ID != rewriteNode.ID {
		t.Fatalf("canonical head = %s, want rewrite node %s", lineage[len(lineage)-1].ID, rewriteNode.ID)
	}
	for _, node := range lineage {
		if node.ID == failedToolNode.ID {
			t.Fatalf("failed tool node should not remain on canonical lineage after rewrite")
		}
	}

	storedRewrite, err := agent.Graph().Node(rewriteNode.ID)
	if err != nil {
		t.Fatalf("Node() error = %v", err)
	}
	if len(storedRewrite.SynthesizedFrom) != 1 || storedRewrite.SynthesizedFrom[0] != failedToolNode.ID {
		t.Fatalf("rewrite synthesized_from = %v, want [%s]", storedRewrite.SynthesizedFrom, failedToolNode.ID)
	}
}

func TestSummarizeParallelToolCallsRewritesSiblingLeaves(t *testing.T) {
	t.Parallel()

	agent, root, err := New("You are a coding agent.")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	taskNode, err := agent.AddUserTask(root.ID, "Search for all references to ParseSpecVersion and Parse.")
	if err != nil {
		t.Fatalf("AddUserTask() error = %v", err)
	}

	planNode, err := agent.AddAssistantPlan(taskNode.ID, "I will search the codebase in parallel.")
	if err != nil {
		t.Fatalf("AddAssistantPlan() error = %v", err)
	}

	grepNode, err := agent.AddToolResult(planNode.ID, "grep", "found ParseSpecVersion in pkg/oac/oac.go", true)
	if err != nil {
		t.Fatalf("AddToolResult(grep) error = %v", err)
	}

	indexNode, err := agent.AddToolResult(planNode.ID, "search-index", "found Parse in pkg/oac/oac.go and tests", true)
	if err != nil {
		t.Fatalf("AddToolResult(search-index) error = %v", err)
	}

	summaryNode, err := agent.SummarizeParallelToolCalls(
		planNode.ID,
		"Parallel search found both parser entry points in pkg/oac/oac.go and related tests.",
		grepNode.ID,
		indexNode.ID,
	)
	if err != nil {
		t.Fatalf("SummarizeParallelToolCalls() error = %v", err)
	}

	storedSummary, err := agent.Graph().Node(summaryNode.ID)
	if err != nil {
		t.Fatalf("Node() error = %v", err)
	}
	if storedSummary.Message.Role != sgp.MessageRoleAssistant {
		t.Fatalf("summary role = %s, want assistant", storedSummary.Message.Role)
	}
	if len(storedSummary.ParentIDs) != 1 || storedSummary.ParentIDs[0] != planNode.ID {
		t.Fatalf("summary parent_ids = %v, want [%s]", storedSummary.ParentIDs, planNode.ID)
	}
	if len(storedSummary.SynthesizedFrom) != 2 {
		t.Fatalf("summary synthesized_from len = %d, want 2", len(storedSummary.SynthesizedFrom))
	}

	lineage, err := agent.Graph().ResumeNodes(summaryNode.ID)
	if err != nil {
		t.Fatalf("ResumeNodes() error = %v", err)
	}
	for _, node := range lineage {
		if node.ID == grepNode.ID || node.ID == indexNode.ID {
			t.Fatalf("parallel tool leaves should not remain on canonical lineage after summary rewrite")
		}
	}
	if lineage[len(lineage)-1].ID != summaryNode.ID {
		t.Fatalf("canonical head = %s, want summary node %s", lineage[len(lineage)-1].ID, summaryNode.ID)
	}
}
