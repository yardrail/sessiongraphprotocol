package sgp

import (
	"errors"
	"testing"
)

func TestNewGraphHasNoEventsBeforeStart(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1")))

	events := graph.Events()
	if len(events) != 0 {
		t.Fatalf("expected no events before Start, got %d", len(events))
	}
}

func TestStartEmitsConfigurableSessionStart(t *testing.T) {
	t.Parallel()

	graph := NewGraph(
		WithIDGenerator(sequenceIDs("session-1")),
		WithEventNames(EventNames{
			SessionStart:     "sgp.session.started",
			NodeAppended:     "sgp.node.appended",
			HistoryRewritten: "sgp.history.rewritten",
			SessionEnded:     "sgp.session.ended",
		}),
	)

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	events := graph.Events()
	if len(events) != 1 {
		t.Fatalf("expected a single session start event, got %d", len(events))
	}

	if got, want := events[0].Event, "sgp.session.started"; got != want {
		t.Fatalf("expected custom start event %q, got %q", want, got)
	}

	if got, want := events[0].SessionID, ID("session-1"); got != want {
		t.Fatalf("expected session id %q, got %q", want, got)
	}
}

func TestStartIsIdempotentError(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("first start: %v", err)
	}

	_, err = graph.Start()
	if !errors.Is(err, ErrSessionAlreadyStarted) {
		t.Fatalf("expected ErrSessionAlreadyStarted on second Start, got %v", err)
	}
}

func TestStartOnClosedGraphReturnsErrSessionClosed(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, _, err = graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, err = graph.End(EndReasonComplete)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	_, err = graph.Start()
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed on Start after End, got %v", err)
	}
}

func TestAppendBeforeStartReturnsErrSessionNotStarted(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))

	_, _, err := graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if !errors.Is(err, ErrSessionNotStarted) {
		t.Fatalf("expected ErrSessionNotStarted, got %v", err)
	}
}

func TestRewriteBeforeStartReturnsErrSessionNotStarted(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1")))

	_, _, err := graph.Rewrite(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "merged"}}}},
		},
		"some-parent",
		"some-source",
	)
	if !errors.Is(err, ErrSessionNotStarted) {
		t.Fatalf("expected ErrSessionNotStarted, got %v", err)
	}
}

func TestEndBeforeStartReturnsErrSessionNotStarted(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1")))

	_, err := graph.End(EndReasonFailed)
	if !errors.Is(err, ErrSessionNotStarted) {
		t.Fatalf("expected ErrSessionNotStarted, got %v", err)
	}
}

func TestEndWithoutNodesSucceedsAfterStart(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	event, err := graph.End(EndReasonFailed)
	if err != nil {
		t.Fatalf("end without nodes: %v", err)
	}

	if got, want := event.Reason, EndReasonFailed; got != want {
		t.Fatalf("expected reason %q, got %q", want, got)
	}

	if event.TerminalNodeID != "" {
		t.Fatalf("expected empty terminal_node_id when no nodes, got %q", event.TerminalNodeID)
	}
}

func TestResumeMessagesReturnsCanonicalLineage(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a", "node-b", "node-c")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "system"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	userNode, _, err := graph.Append(
		Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "hello"}}}}},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	assistantNode, _, err := graph.Append(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "world"}}}},
		},
		userNode.ID,
	)
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	messages, err := graph.ResumeMessages(assistantNode.ID)
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	if got, want := messages[0].TextContent(), "system"; got != want {
		t.Fatalf("expected first message %q, got %v", want, got)
	}

	if got, want := messages[2].TextContent(), "world"; got != want {
		t.Fatalf("expected final message %q, got %v", want, got)
	}
}

func TestRewriteKeepsBranchHistoryOutOfCanonicalResume(t *testing.T) {
	t.Parallel()

	graph := NewGraph(
		WithIDGenerator(sequenceIDs(
			"session-1", "a", "b", "c", "d1", "d2", "e1", "f",
		)),
	)

	mustStart(t, graph)

	root, _ := mustAppend(t, graph, Message{System: &SystemMessage{Text: "sys"}})
	userNode, _ := mustAppend(t, graph,
		Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "user"}}}}},
		root.ID,
	)
	canonicalNode, _ := mustAppend(
		t,
		graph,
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "think"}}}},
		},
		userNode.ID,
	)
	branchOne, _ := mustAppend(
		t,
		graph,
		Message{
			Assistant: &AssistantMessage{
				Parts: []ContentPart{{Text: &TextPart{Text: "branch one"}}},
			},
		},
		canonicalNode.ID,
	)
	branchTwo, _ := mustAppend(
		t,
		graph,
		Message{
			Assistant: &AssistantMessage{
				Parts: []ContentPart{{Text: &TextPart{Text: "branch two"}}},
			},
		},
		canonicalNode.ID,
	)

	rewriteNode, event := mustRewrite(
		t,
		graph,
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "merged"}}}},
		},
		canonicalNode.ID,
		branchOne.ID,
		branchTwo.ID,
	)

	if got, want := event.Event, DefaultEventNames().HistoryRewritten; got != want {
		t.Fatalf("expected rewrite event %q, got %q", want, got)
	}

	lineage, err := graph.ResumeNodes(rewriteNode.ID)
	if err != nil {
		t.Fatalf("resume nodes: %v", err)
	}

	if len(lineage) != 4 {
		t.Fatalf("expected canonical lineage length 4, got %d", len(lineage))
	}

	if got, want := lineage[3].Message.TextContent(), "merged"; got != want {
		t.Fatalf("expected rewrite message %q, got %v", want, got)
	}

	if len(lineage[3].SynthesizedFrom) != 2 {
		t.Fatalf(
			"expected rewrite node to preserve synthesized sources, got %d",
			len(lineage[3].SynthesizedFrom),
		)
	}
}

func TestNeedsResponseOnlyForDanglingUserOrToolLeaves(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a", "node-b", "node-c")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	userNode, _, err := graph.Append(
		Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "ask"}}}}},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	needsResponse, err := graph.NeedsResponse(userNode.ID)
	if err != nil {
		t.Fatalf("needs response before assistant: %v", err)
	}

	if !needsResponse {
		t.Fatal("expected dangling user leaf to require a response")
	}

	_, _, err = graph.Append(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "answer"}}}},
		},
		userNode.ID,
	)
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	needsResponse, err = graph.NeedsResponse(userNode.ID)
	if err != nil {
		t.Fatalf("needs response after assistant: %v", err)
	}

	if needsResponse {
		t.Fatal("expected non-leaf user node to stop requiring a response")
	}
}

func TestEndUsesCurrentHead(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	event, err := graph.End(EndReasonComplete)
	if err != nil {
		t.Fatalf("end graph: %v", err)
	}

	if got, want := event.TerminalNodeID, root.ID; got != want {
		t.Fatalf("expected terminal node %q, got %q", want, got)
	}

	_, _, err = graph.Append(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "late"}}}},
		},
		root.ID,
	)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func mustStart(t *testing.T, g *Graph) Event {
	t.Helper()

	ev, err := g.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	return ev
}

func mustAppend(t *testing.T, g *Graph, msg Message, parentIDs ...ID) (Node, Event) {
	t.Helper()

	node, ev, err := g.Append(msg, parentIDs...)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	return node, ev
}

func mustRewrite(t *testing.T, g *Graph, msg Message, parentID ID, replaces ...ID) (Node, Event) {
	t.Helper()

	node, ev, err := g.Rewrite(msg, parentID, replaces...)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	return node, ev
}

func sequenceIDs(ids ...ID) IDGenerator {
	index := 0

	return func() ID {
		if index >= len(ids) {
			panic("sequenceIDs exhausted")
		}

		id := ids[index]
		index++

		return id
	}
}
