package liveagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
	"google.golang.org/genai"
)

func TestExecuteFunctionCallsPrunesSingleFailedTool(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	store, err := sgp.NewJSONFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewJSONFileStore() error = %v", err)
	}

	console, err := NewConsole(workspace, store, "system")
	if err != nil {
		t.Fatalf("NewConsole() error = %v", err)
	}

	userNode, err := console.agent.AddUserTask(console.headID, "read the missing file")
	if err != nil {
		t.Fatalf("AddUserTask() error = %v", err)
	}
	console.headID = userNode.ID

	planNode, err := console.agent.AddAssistantPlan(console.headID, "Attempt read_file")
	if err != nil {
		t.Fatalf("AddAssistantPlan() error = %v", err)
	}
	console.headID = planNode.ID

	outcomes, _, err := console.executeFunctionCalls(ctx, planNode.ID, []*genai.FunctionCall{{
		Name: "read_file",
		Args: map[string]any{"path": "missing.txt", "start_line": float64(1), "end_line": float64(5)},
	}})
	if err != nil {
		t.Fatalf("executeFunctionCalls() error = %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("len(outcomes) = %d, want 1", len(outcomes))
	}
	if outcomes[0].success {
		t.Fatalf("outcomes[0].success = true, want false")
	}

	head, ok := console.agent.Graph().Head()
	if !ok {
		t.Fatal("Head() returned no head")
	}
	if got := head.Metadata["kind"]; got != "failed_tool_pruned" {
		t.Fatalf("head.Metadata[kind] = %v, want failed_tool_pruned", got)
	}
}

func TestExecuteFunctionCallsSummarizesParallelBatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "alpha.txt"), []byte("first\nneedle here\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(alpha.txt) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "beta.txt"), []byte("second\nneedle there\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(beta.txt) error = %v", err)
	}

	console, err := NewConsole(workspace, nil, "system")
	if err != nil {
		t.Fatalf("NewConsole() error = %v", err)
	}

	userNode, err := console.agent.AddUserTask(console.headID, "inspect files")
	if err != nil {
		t.Fatalf("AddUserTask() error = %v", err)
	}
	console.headID = userNode.ID

	planNode, err := console.agent.AddAssistantPlan(console.headID, "Run list and grep")
	if err != nil {
		t.Fatalf("AddAssistantPlan() error = %v", err)
	}
	console.headID = planNode.ID

	outcomes, _, err := console.executeFunctionCalls(ctx, planNode.ID, []*genai.FunctionCall{
		{Name: "list_files", Args: map[string]any{"limit": float64(10)}},
		{Name: "grep_text", Args: map[string]any{"query": "needle", "limit": float64(10)}},
	})
	if err != nil {
		t.Fatalf("executeFunctionCalls() error = %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("len(outcomes) = %d, want 2", len(outcomes))
	}

	head, ok := console.agent.Graph().Head()
	if !ok {
		t.Fatal("Head() returned no head")
	}
	if got := head.Metadata["kind"]; got != "parallel_tool_summary" {
		t.Fatalf("head.Metadata[kind] = %v, want parallel_tool_summary", got)
	}
	if got := head.Metadata["branches"]; got != 2 {
		t.Fatalf("head.Metadata[branches] = %v, want 2", got)
	}
}

func TestSpawnSubagentSearchCarriesProvenance(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("subagent needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(notes.txt) error = %v", err)
	}

	console, err := NewConsole(workspace, nil, "system")
	if err != nil {
		t.Fatalf("NewConsole() error = %v", err)
	}

	parentNode, err := console.agent.AddAssistantPlan(console.headID, "delegate broad search")
	if err != nil {
		t.Fatalf("AddAssistantPlan() error = %v", err)
	}
	console.headID = parentNode.ID

	outcome := console.executeFunctionCall(ctx, parentNode.ID, &genai.FunctionCall{
		Name: "spawn_subagent_search",
		Args: map[string]any{"question": "where is needle mentioned?", "query": "needle", "limit": float64(5)},
	})
	if !outcome.success {
		t.Fatalf("executeFunctionCall() success = false, output = %s", outcome.output)
	}
	if outcome.subgraph == nil {
		t.Fatal("outcome.subgraph = nil, want spawned subgraph")
	}

	spawnedFrom := outcome.subgraph.Graph().Session().SpawnedFrom
	if spawnedFrom == nil {
		t.Fatal("SpawnedFrom = nil, want provenance")
	}
	if spawnedFrom.NodeID != parentNode.ID {
		t.Fatalf("SpawnedFrom.NodeID = %s, want %s", spawnedFrom.NodeID, parentNode.ID)
	}
	if spawnedFrom.SessionID != console.agent.Graph().Session().ID {
		t.Fatalf("SpawnedFrom.SessionID = %s, want %s", spawnedFrom.SessionID, console.agent.Graph().Session().ID)
	}
	if outcome.payload == nil {
		t.Fatal("outcome.payload = nil, want search payload")
	}
}
