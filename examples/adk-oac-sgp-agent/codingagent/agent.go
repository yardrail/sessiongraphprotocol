package codingagent

import (
	"fmt"

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
)

// Agent is a proof-of-concept harness that applies SGP rewrite semantics to
// coding-agent workflows such as subagents, failed tool pruning, and parallel
// tool summarization.
type Agent struct {
	graph *sgp.Graph
}

// New creates a root coding-agent session graph.
func New(systemPrompt string) (*Agent, sgp.Node, error) {
	graph := sgp.NewGraph()

	root, _, err := graph.Append(sgp.Message{
		Role:    sgp.MessageRoleSystem,
		Content: systemPrompt,
	}, map[string]any{"kind": "system_prompt"})
	if err != nil {
		return nil, sgp.Node{}, err
	}

	return &Agent{graph: graph}, root, nil
}

// Graph returns the underlying SGP graph.
func (agent *Agent) Graph() *sgp.Graph {
	return agent.graph
}

// AddUserTask appends a user task to the canonical history.
func (agent *Agent) AddUserTask(parentID sgp.ID, task string) (sgp.Node, error) {
	node, _, err := agent.graph.Append(sgp.Message{
		Role:    sgp.MessageRoleUser,
		Content: task,
	}, map[string]any{"kind": "coding_task"}, parentID)
	if err != nil {
		return sgp.Node{}, err
	}

	return node, nil
}

// AddAssistantPlan appends an assistant planning node.
func (agent *Agent) AddAssistantPlan(parentID sgp.ID, plan string) (sgp.Node, error) {
	node, _, err := agent.graph.Append(sgp.Message{
		Role:    sgp.MessageRoleAssistant,
		Content: plan,
	}, map[string]any{"kind": "plan"}, parentID)
	if err != nil {
		return sgp.Node{}, err
	}

	return node, nil
}

// AddToolResult appends a tool result. Multiple calls against the same parent
// model parallel tool calls as sibling leaves.
func (agent *Agent) AddToolResult(parentID sgp.ID, toolName string, output string, success bool) (sgp.Node, error) {
	metadata := map[string]any{
		"kind":    "tool_result",
		"tool":    toolName,
		"success": success,
	}

	node, _, err := agent.graph.Append(sgp.Message{
		Role:    sgp.MessageRoleTool,
		Content: output,
	}, metadata, parentID)
	if err != nil {
		return sgp.Node{}, err
	}

	return node, nil
}

// SpawnSubagent creates a new SGP subagent session with spawned_from provenance.
func (agent *Agent) SpawnSubagent(parentNodeID sgp.ID, systemPrompt string, task string) (*Agent, sgp.Node, sgp.Node, error) {
	subgraph := sgp.NewGraph(
		sgp.WithSpawnedFrom(sgp.SpawnReference{
			SessionID: agent.graph.Session().ID,
			NodeID:    parentNodeID,
		}),
	)

	root, _, err := subgraph.Append(sgp.Message{
		Role:    sgp.MessageRoleSystem,
		Content: systemPrompt,
	}, map[string]any{"kind": "subagent_system"})
	if err != nil {
		return nil, sgp.Node{}, sgp.Node{}, err
	}

	userTask, _, err := subgraph.Append(sgp.Message{
		Role:    sgp.MessageRoleUser,
		Content: task,
	}, map[string]any{"kind": "subagent_task"}, root.ID)
	if err != nil {
		return nil, sgp.Node{}, sgp.Node{}, err
	}

	return &Agent{graph: subgraph}, root, userTask, nil
}

// PruneFailedToolCall rewrites history so a failed tool-result leaf is retained
// only as audit provenance and no longer sits on the canonical resume path.
func (agent *Agent) PruneFailedToolCall(canonicalParentID, failedToolNodeID sgp.ID, summary string) (sgp.Node, error) {
	node, _, err := agent.graph.Rewrite(sgp.Message{
		Role:    sgp.MessageRoleAssistant,
		Content: summary,
	}, map[string]any{
		"kind":   "failed_tool_pruned",
		"reason": "tool_failure",
	}, canonicalParentID, failedToolNodeID)
	if err != nil {
		return sgp.Node{}, fmt.Errorf("prune failed tool call: %w", err)
	}

	return node, nil
}

// SummarizeParallelToolCalls rewrites sibling tool leaves into one assistant
// summary node so future inference resumes from compacted canonical history.
func (agent *Agent) SummarizeParallelToolCalls(canonicalParentID sgp.ID, summary string, branchTipIDs ...sgp.ID) (sgp.Node, error) {
	node, _, err := agent.graph.Rewrite(sgp.Message{
		Role:    sgp.MessageRoleAssistant,
		Content: summary,
	}, map[string]any{
		"kind":     "parallel_tool_summary",
		"branches": len(branchTipIDs),
	}, canonicalParentID, branchTipIDs...)
	if err != nil {
		return sgp.Node{}, fmt.Errorf("summarize parallel tool calls: %w", err)
	}

	return node, nil
}
