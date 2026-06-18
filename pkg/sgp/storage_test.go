package sgp

import (
	"errors"
	"testing"
	"time"
)

// --- ClassifyEvent ---

func TestClassifyEventSessionStart(t *testing.T) {
	t.Parallel()

	event := Event{Event: "session.start", SessionID: "s1"}
	if got, want := ClassifyEvent(event), EventKindSessionStart; got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestClassifyEventNodeAppended(t *testing.T) {
	t.Parallel()

	event := Event{
		Event: "node.appended",
		Node: &Node{
			ID:        "n1",
			SessionID: "s1",
			Message:   Message{System: &SystemMessage{Text: "sys"}},
		},
	}
	if got, want := ClassifyEvent(event), EventKindNodeAppended; got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestClassifyEventHistoryRewritten(t *testing.T) {
	t.Parallel()

	event := Event{
		Event: "history.rewritten",
		Node: &Node{
			ID:              "n2",
			SessionID:       "s1",
			SynthesizedFrom: []ID{"n1"},
			Message: Message{
				Assistant: &AssistantMessage{
					Parts: []ContentPart{{Text: &TextPart{Text: "merged"}}},
				},
			},
		},
	}
	if got, want := ClassifyEvent(event), EventKindHistoryRewritten; got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestClassifyEventSessionEndedByReason(t *testing.T) {
	t.Parallel()

	event := Event{Event: "session.ended", Reason: EndReasonFailed}
	if got, want := ClassifyEvent(event), EventKindSessionEnded; got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestClassifyEventSessionEndedByTerminalNodeID(t *testing.T) {
	t.Parallel()

	event := Event{Event: "session.ended", TerminalNodeID: "n1", Reason: EndReasonComplete}
	if got, want := ClassifyEvent(event), EventKindSessionEnded; got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

// --- RestoreFromEvents ---

func TestRestoreFromEventsEmptyReturnsNotFound(t *testing.T) {
	t.Parallel()

	_, err := RestoreFromEvents(nil)
	if !errors.Is(err, ErrGraphNotFound) {
		t.Fatalf("expected ErrGraphNotFound, got %v", err)
	}

	_, err = RestoreFromEvents(make([]Event, 0))
	if !errors.Is(err, ErrGraphNotFound) {
		t.Fatalf("expected ErrGraphNotFound for empty slice, got %v", err)
	}
}

func TestRestoreFromEventsMissingSessionStart(t *testing.T) {
	t.Parallel()

	// Events without a session.start event.
	events := []Event{
		{
			Event: DefaultEventNames().NodeAppended,
			Node: &Node{
				ID:        "n1",
				SessionID: "s1",
				Message:   Message{System: &SystemMessage{Text: "sys"}},
			},
		},
	}

	_, err := RestoreFromEvents(events)
	if err == nil {
		t.Fatal("expected error for missing session.start, got nil")
	}
}

func TestRestoreFromEventsStartedOnlyGraphAcceptsAppends(t *testing.T) {
	t.Parallel()

	// A session that started but has no nodes yet (e.g. provisioning in progress).
	graph := NewGraph(WithIDGenerator(sequenceIDs("s1", "n1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	restored, err := RestoreFromEvents([]Event{startEvent})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	_, _, err = restored.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("expected restored open graph to accept appends, got %v", err)
	}
}

func TestRestoreFromEventsFailedSessionNoNodes(t *testing.T) {
	t.Parallel()

	// Session started then immediately failed (provisioning failure, no nodes).
	graph := NewGraph(WithIDGenerator(sequenceIDs("s1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	endEvent, err := graph.End(EndReasonFailed)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	restored, err := RestoreFromEvents([]Event{startEvent, endEvent})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	_, _, err = restored.Append(Message{System: &SystemMessage{Text: "sys"}})
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed on failed session, got %v", err)
	}
}

func TestRestoreFromEventsLinearSession(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("s1", "n-sys", "n-user", "n-asst")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "system prompt"}})
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

	_, _, err = graph.Append(
		Message{Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "hi"}}}}},
		userNode.ID,
	)
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	assertLinearSessionMessages(t, restored)
}

func assertLinearSessionMessages(t *testing.T, g *Graph) {
	t.Helper()

	head, ok := g.Head()
	if !ok {
		t.Fatal("expected restored graph to have a head node")
	}

	messages, err := g.ResumeMessages(head.ID)
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if got, want := len(messages), 3; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}

	if got, want := messages[0].TextContent(), "system prompt"; got != want {
		t.Fatalf("expected first message %q, got %q", want, got)
	}

	if got, want := messages[2].TextContent(), "hi"; got != want {
		t.Fatalf("expected last message %q, got %q", want, got)
	}
}

func TestRestoreFromEventsDanglingLeafDetected(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("s1", "n-sys", "n-user")))

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

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	needsResponse, err := restored.NeedsResponse(userNode.ID)
	if err != nil {
		t.Fatalf("needs response: %v", err)
	}

	if !needsResponse {
		t.Fatal("expected dangling user leaf to require a response after restore")
	}
}

func TestRestoreFromEventsClosedGraphRejectsAppends(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("s1", "n1")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, err = graph.End(EndReasonComplete)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	_, _, err = restored.Append(Message{User: &UserMessage{}}, root.ID)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", err)
	}
}

func TestRestoreFromEventsEndReasonPreserved(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("s1", "n1")))

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, _, err = graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, err = graph.End(EndReasonFailed)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Confirm the closed graph carries the right reason by attempting a second End.
	_, err = restored.End(EndReasonComplete)
	if !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected closed graph, got %v", err)
	}

	// The session.ended event in the restored graph must carry the reason.
	var foundReason EndReason

	for _, e := range restored.Events() {
		if e.Kind == EventKindSessionEnded {
			foundReason = e.Reason
		}
	}

	if got, want := foundReason, EndReasonFailed; got != want {
		t.Fatalf("expected end reason %q, got %q", want, got)
	}
}

func buildHistoryRewriteGraph(t *testing.T) (*Graph, Node) {
	t.Helper()

	graph := NewGraph(
		WithIDGenerator(sequenceIDs("s1", "n-sys", "n-user", "n-b1", "n-b2", "n-merge")),
	)

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, _, err := graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	canonical, _, err := graph.Append(
		Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "ask"}}}}},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append canonical: %v", err)
	}

	b1, _, err := graph.Append(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "branch-1"}}}},
		},
		canonical.ID,
	)
	if err != nil {
		t.Fatalf("append b1: %v", err)
	}

	b2, _, err := graph.Append(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "branch-2"}}}},
		},
		canonical.ID,
	)
	if err != nil {
		t.Fatalf("append b2: %v", err)
	}

	mergeNode, _, err := graph.Rewrite(
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "merged"}}}},
		},
		canonical.ID,
		b1.ID,
		b2.ID,
	)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	return graph, mergeNode
}

func TestRestoreFromEventsHistoryRewrite(t *testing.T) {
	t.Parallel()

	graph, mergeNode := buildHistoryRewriteGraph(t)

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	lineage, err := restored.ResumeNodes(mergeNode.ID)
	if err != nil {
		t.Fatalf("resume nodes: %v", err)
	}

	// Canonical lineage: root → canonical → mergeNode (branches excluded).
	if got, want := len(lineage), 3; got != want {
		t.Fatalf("expected canonical lineage length %d, got %d", want, got)
	}

	if got, want := lineage[2].Message.TextContent(), "merged"; got != want {
		t.Fatalf("expected merge node content %q, got %q", want, got)
	}

	if got, want := len(lineage[2].SynthesizedFrom), 2; got != want {
		t.Fatalf("expected 2 synthesized sources, got %d", got)
	}
}

func TestRestoreFromEventsSubagentSpawnedFrom(t *testing.T) {
	t.Parallel()

	spawnRef := SpawnReference{SessionID: "parent-session", NodeID: "parent-node"}

	graph := NewGraph(
		WithIDGenerator(sequenceIDs("child-session", "n1")),
		WithSpawnedFrom(spawnRef),
	)

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, _, err = graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	session := restored.Session()
	if session.SpawnedFrom == nil {
		t.Fatal("expected spawned_from to be restored")
	}

	if got, want := session.SpawnedFrom.SessionID, ID("parent-session"); got != want {
		t.Fatalf("expected parent session id %q, got %q", want, got)
	}

	if got, want := session.SpawnedFrom.NodeID, ID("parent-node"); got != want {
		t.Fatalf("expected parent node id %q, got %q", want, got)
	}
}

func TestRestoreFromEventsCustomEventNames(t *testing.T) {
	t.Parallel()

	customNames := EventNames{
		SessionStart:     "sgp.session.started",
		NodeAppended:     "sgp.node.appended",
		HistoryRewritten: "sgp.history.rewritten",
		SessionEnded:     "sgp.session.ended",
	}

	graph := NewGraph(
		WithIDGenerator(sequenceIDs("s1", "n1")),
		WithEventNames(customNames),
	)

	_, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, _, err = graph.Append(Message{System: &SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	restored, err := RestoreFromEvents(graph.Events())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Further appends should use the inferred custom event names.
	head, _ := restored.Head()

	_, appendEvent, err := restored.Append(
		Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "hi"}}}}},
		head.ID,
	)
	if err != nil {
		t.Fatalf("append on restored graph: %v", err)
	}

	if got, want := appendEvent.Event, customNames.NodeAppended; got != want {
		t.Fatalf("expected custom event name %q, got %q", want, got)
	}
}

func TestRestoreFromEventsDuplicateSessionStartReturnsError(t *testing.T) {
	t.Parallel()

	startEvent := Event{
		Kind:      EventKindSessionStart,
		Event:     DefaultEventNames().SessionStart,
		SessionID: "s1",
	}

	_, err := RestoreFromEvents([]Event{startEvent, startEvent})
	if err == nil {
		t.Fatal("expected error for duplicate session.start, got nil")
	}
}

func TestRestoreFromEventsMissingParentReturnsError(t *testing.T) {
	t.Parallel()

	startEvent := Event{
		Kind:      EventKindSessionStart,
		Event:     DefaultEventNames().SessionStart,
		SessionID: "s1",
	}

	nodeEvent := Event{
		Kind:  EventKindNodeAppended,
		Event: DefaultEventNames().NodeAppended,
		Node: &Node{
			ID:        "n1",
			SessionID: "s1",
			ParentIDs: []ID{"missing-parent"},
			Message:   Message{User: &UserMessage{}},
		},
	}

	_, err := RestoreFromEvents([]Event{startEvent, nodeEvent})
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

// --- RestoreFromNodes ---

func TestRestoreFromNodesLinear(t *testing.T) {
	t.Parallel()

	sess := Session{ID: "s1", Timestamp: time.Now().UTC()}
	n1 := Node{ID: "n1", SessionID: "s1", Timestamp: time.Now().UTC(),
		Message: Message{System: &SystemMessage{Text: "system prompt"}}}
	n2 := Node{ID: "n2", SessionID: "s1", Timestamp: time.Now().Add(time.Millisecond).UTC(),
		ParentIDs: []ID{"n1"},
		Message:   Message{User: &UserMessage{Parts: []ContentPart{{Text: &TextPart{Text: "hello"}}}}}}
	n3 := Node{ID: "n3", SessionID: "s1", Timestamp: time.Now().Add(2 * time.Millisecond).UTC(),
		ParentIDs: []ID{"n2"},
		Message:   Message{Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "hi"}}}}}}

	g, err := RestoreFromNodes(sess, []Node{n3, n1, n2}, "n3", SessionStatusOpen, "", "")
	if err != nil {
		t.Fatalf("RestoreFromNodes: %v", err)
	}

	assertLinearSessionMessages(t, g)
}

func TestRestoreFromNodesRewrite(t *testing.T) {
	t.Parallel()

	graph, mergeNode := buildHistoryRewriteGraph(t)
	sess := graph.Session()
	var nodes []Node
	for _, e := range graph.Events() {
		if e.Node != nil {
			nodes = append(nodes, *e.Node)
		}
	}

	g, err := RestoreFromNodes(sess, nodes, mergeNode.ID, SessionStatusOpen, "", "")
	if err != nil {
		t.Fatalf("RestoreFromNodes: %v", err)
	}

	lineage, err := g.ResumeNodes(mergeNode.ID)
	if err != nil {
		t.Fatalf("ResumeNodes: %v", err)
	}
	if got, want := len(lineage), 3; got != want {
		t.Fatalf("expected canonical lineage length %d, got %d", want, got)
	}
	if lineage[2].Message.TextContent() != "merged" {
		t.Fatalf("unexpected merge node content: %q", lineage[2].Message.TextContent())
	}
}

func TestRestoreFromNodesSubagentSpawn(t *testing.T) {
	t.Parallel()

	spawnRef := SpawnReference{SessionID: "parent-session", NodeID: "parent-node"}
	sess := Session{
		ID:          "child-session",
		Timestamp:   time.Now().UTC(),
		SpawnedFrom: &spawnRef,
	}
	nodes := []Node{{
		ID: "n1", SessionID: "child-session", Timestamp: time.Now().UTC(),
		Message: Message{System: &SystemMessage{Text: "sys"}},
	}}

	g, err := RestoreFromNodes(sess, nodes, "n1", SessionStatusOpen, "", "")
	if err != nil {
		t.Fatalf("RestoreFromNodes: %v", err)
	}

	s := g.Session()
	if s.SpawnedFrom == nil {
		t.Fatal("expected spawned_from to be set")
	}
	if s.SpawnedFrom.SessionID != "parent-session" {
		t.Fatalf("expected parent-session, got %q", s.SpawnedFrom.SessionID)
	}
}

func TestRestoreFromNodesClosed(t *testing.T) {
	t.Parallel()

	sess := Session{ID: "s1", Timestamp: time.Now().UTC()}
	n1 := Node{ID: "n1", SessionID: "s1", Timestamp: time.Now().UTC(),
		Message: Message{System: &SystemMessage{Text: "sys"}}}

	g, err := RestoreFromNodes(sess, []Node{n1}, "n1", SessionStatusClosed, EndReasonComplete, "n1")
	if err != nil {
		t.Fatalf("RestoreFromNodes: %v", err)
	}

	_, _, appendErr := g.Append(Message{User: &UserMessage{}}, "n1")
	if !errors.Is(appendErr, ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed, got %v", appendErr)
	}
}

func TestRestoreFromNodesFanOut(t *testing.T) {
	t.Parallel()

	sess := Session{ID: "s1", Timestamp: time.Now().UTC()}
	root := Node{ID: "root", SessionID: "s1", Timestamp: time.Now().UTC(),
		Message: Message{System: &SystemMessage{Text: "sys"}}}
	b1 := Node{ID: "b1", SessionID: "s1", Timestamp: time.Now().Add(time.Millisecond).UTC(),
		ParentIDs: []ID{"root"},
		Message:   Message{Assistant: &AssistantMessage{}}}
	b2 := Node{ID: "b2", SessionID: "s1", Timestamp: time.Now().Add(2 * time.Millisecond).UTC(),
		ParentIDs: []ID{"root"},
		Message:   Message{Assistant: &AssistantMessage{}}}

	// Provide in reversed/shuffled order to test topo sort
	g, err := RestoreFromNodes(sess, []Node{b2, b1, root}, "b2", SessionStatusOpen, "", "")
	if err != nil {
		t.Fatalf("RestoreFromNodes fan-out: %v", err)
	}

	// Both branches should be accessible
	if _, err := g.Node("b1"); err != nil {
		t.Fatalf("b1 not in graph: %v", err)
	}
	if _, err := g.Node("b2"); err != nil {
		t.Fatalf("b2 not in graph: %v", err)
	}
}

func TestRestoreFromNodesEmpty(t *testing.T) {
	t.Parallel()

	sess := Session{ID: "s1", Timestamp: time.Now().UTC()}
	g, err := RestoreFromNodes(sess, nil, "", SessionStatusOpen, "", "")
	if err != nil {
		t.Fatalf("RestoreFromNodes with no nodes: %v", err)
	}

	_, ok := g.Head()
	if ok {
		t.Fatal("expected no head for empty graph")
	}
}

func TestRestoreFromNodesCycleDetected(t *testing.T) {
	t.Parallel()

	sess := Session{ID: "s1", Timestamp: time.Now().UTC()}
	// n1 -> n2 -> n1 (cycle)
	n1 := Node{ID: "n1", SessionID: "s1", Timestamp: time.Now().UTC(),
		ParentIDs: []ID{"n2"},
		Message:   Message{System: &SystemMessage{Text: "sys"}}}
	n2 := Node{ID: "n2", SessionID: "s1", Timestamp: time.Now().Add(time.Millisecond).UTC(),
		ParentIDs: []ID{"n1"},
		Message:   Message{System: &SystemMessage{Text: "sys"}}}

	_, err := RestoreFromNodes(sess, []Node{n1, n2}, "n2", SessionStatusOpen, "", "")
	if err == nil {
		t.Fatal("expected error for cycle, got nil")
	}
}
