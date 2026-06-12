package sgp

import (
	"errors"
	"testing"
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
