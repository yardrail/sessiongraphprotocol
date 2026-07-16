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
			SessionStart: "sgp.session.started",
			NodeAppended: "sgp.node.appended",
			SessionEnded: "sgp.session.ended",
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

func TestBranchesExcludedFromCanonicalResume(t *testing.T) {
	t.Parallel()

	graph := NewGraph(
		WithIDGenerator(sequenceIDs(
			"session-1", "a", "b", "c", "d1", "d2", "e",
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
	// Two dead-end branches off canonicalNode.
	mustAppend(t, graph,
		Message{Assistant: &AssistantMessage{
			Parts: []ContentPart{{Text: &TextPart{Text: "branch one"}}},
		}},
		canonicalNode.ID,
	)
	mustAppend(t, graph,
		Message{Assistant: &AssistantMessage{
			Parts: []ContentPart{{Text: &TextPart{Text: "branch two"}}},
		}},
		canonicalNode.ID,
	)

	// Canonical continuation from canonicalNode.
	continueNode, _ := mustAppend(
		t,
		graph,
		Message{
			Assistant: &AssistantMessage{Parts: []ContentPart{{Text: &TextPart{Text: "canonical"}}}},
		},
		canonicalNode.ID,
	)

	lineage, err := graph.ResumeNodes(continueNode.ID)
	if err != nil {
		t.Fatalf("resume nodes: %v", err)
	}

	if len(lineage) != 4 {
		t.Fatalf("expected canonical lineage length 4, got %d", len(lineage))
	}

	if got, want := lineage[3].Message.TextContent(), "canonical"; got != want {
		t.Fatalf("expected node message %q, got %v", want, got)
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

func TestAdvanceHead(t *testing.T) {
	t.Parallel()

	g := NewGraph()
	mustStart(t, g)
	n1, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})
	n2, _ := mustAppend(t, g, Message{User: &UserMessage{}}, n1.ID)
	n3, _ := mustAppend(t, g, Message{Assistant: &AssistantMessage{}}, n2.ID)

	// Walk from n1 should reach n3.
	got, err := g.AdvanceHead(n1.ID)
	if err != nil || got != n3.ID {
		t.Fatalf("AdvanceHead(n1): got %s, %v; want %s, nil", got, err, n3.ID)
	}

	// Already at head.
	got, err = g.AdvanceHead(n3.ID)
	if err != nil || got != n3.ID {
		t.Fatalf("AdvanceHead(n3): got %s, %v; want %s, nil", got, err, n3.ID)
	}

	// Unknown node.
	_, err = g.AdvanceHead("missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("AdvanceHead(missing): want ErrNodeNotFound, got %v", err)
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

// --- WithSessionID ---

func TestWithSessionID(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithSessionID("my-id"))
	ev := mustStart(t, g)

	if got, want := ev.SessionID, ID("my-id"); got != want {
		t.Fatalf("expected session id %q, got %q", want, got)
	}

	if got := g.Session().ID; got != "my-id" {
		t.Fatalf("Session().ID: got %q, want %q", got, "my-id")
	}
}

// --- Role ---

func TestMessageRole(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  Message
		want MessageRole
	}{
		{"system", Message{System: &SystemMessage{Text: "s"}}, MessageRoleSystem},
		{"user", Message{User: &UserMessage{}}, MessageRoleUser},
		{"assistant", Message{Assistant: &AssistantMessage{}}, MessageRoleAssistant},
		{"tool", Message{Tool: &ToolMessage{Name: "f"}}, MessageRoleTool},
		{"empty", Message{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.msg.Role(); got != tc.want {
				t.Fatalf("Role() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- TextContent ---

func TestTextContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"system", Message{System: &SystemMessage{Text: "hello"}}, "hello"},
		{"user with text part", Message{User: &UserMessage{Parts: []ContentPart{
			{Text: &TextPart{Text: "a"}},
			{Text: &TextPart{Text: "b"}},
		}}}, "ab"},
		{"assistant with text part", Message{Assistant: &AssistantMessage{Parts: []ContentPart{
			{Text: &TextPart{Text: "ans"}},
		}}}, "ans"},
		{"tool with text part", Message{Tool: &ToolMessage{Name: "fn", Parts: []ContentPart{
			{Text: &TextPart{Text: "result"}},
		}}}, "result"},
		{"empty message", Message{}, ""},
		{"user no parts", Message{User: &UserMessage{}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.msg.TextContent(); got != tc.want {
				t.Fatalf("TextContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- EffectiveKind ---

func TestEffectiveKind(t *testing.T) {
	t.Parallel()

	t.Run("empty kind returns experience", func(t *testing.T) {
		t.Parallel()
		n := Node{}
		if got := n.EffectiveKind(); got != NodeKindExperience {
			t.Fatalf("EffectiveKind() = %q, want %q", got, NodeKindExperience)
		}
	})

	t.Run("non-empty kind returned as-is", func(t *testing.T) {
		t.Parallel()
		n := Node{Kind: NodeKindMemory}
		if got := n.EffectiveKind(); got != NodeKindMemory {
			t.Fatalf("EffectiveKind() = %q, want %q", got, NodeKindMemory)
		}
	})
}

// --- EventNames.Name ---

func TestEventNamesName(t *testing.T) {
	t.Parallel()

	names := DefaultEventNames()

	cases := []struct {
		kind EventKind
		want string
	}{
		{EventKindSessionStart, "session.start"},
		{EventKindNodeAppended, "node.appended"},
		{EventKindSessionEnded, "session.ended"},
		{EventKind(99), ""},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := names.Name(tc.kind); got != tc.want {
				t.Fatalf("Name(%d) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

// --- copyMemoryContent ---

func TestCopyMemoryContentDeepCopy(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "mem")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	mem := &MemoryContent{Summary: "s", Tags: []string{"a", "b"}, Importance: 0.9}
	node, _, err := g.AppendTypedNode(NodeKindMemory, Message{User: &UserMessage{}}, nil, mem, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	if got.Memory == nil {
		t.Fatal("expected Memory content, got nil")
	}
	if got.Memory.Summary != "s" {
		t.Fatalf("Summary = %q, want %q", got.Memory.Summary, "s")
	}
	if got.Memory.Importance != 0.9 {
		t.Fatalf("Importance = %v, want 0.9", got.Memory.Importance)
	}
	if len(got.Memory.Tags) != 2 {
		t.Fatalf("Tags len = %d, want 2", len(got.Memory.Tags))
	}

	// Mutate returned tags; re-fetch should be unaffected.
	got.Memory.Tags[0] = "mutated"
	refetched, _ := g.Node(node.ID)
	if refetched.Memory.Tags[0] != "a" {
		t.Fatalf("deep copy violated: got %q, want %q", refetched.Memory.Tags[0], "a")
	}
}

// --- copySkillContent ---

func TestCopySkillContent(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "skill")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	skill := &SkillContent{Name: "n", Description: "d", Procedure: "p"}
	node, _, err := g.AppendTypedNode(NodeKindSkill, Message{User: &UserMessage{}}, nil, skill, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	if got.Skill == nil {
		t.Fatal("expected Skill content, got nil")
	}
	if got.Skill.Name != "n" || got.Skill.Description != "d" || got.Skill.Procedure != "p" {
		t.Fatalf("Skill = %+v, want {n d p}", got.Skill)
	}
}

// --- copyIdentityContent ---

func TestCopyIdentityContentDeepCopy(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "ident")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	ident := &IdentityContent{Traits: []string{"t"}, Values: []string{"v"}, Goals: []string{"g"}}
	node, _, err := g.AppendTypedNode(NodeKindIdentity, Message{User: &UserMessage{}}, nil, ident, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	if got.Identity == nil {
		t.Fatal("expected Identity content, got nil")
	}
	if len(got.Identity.Traits) != 1 || got.Identity.Traits[0] != "t" {
		t.Fatalf("Traits = %v, want [t]", got.Identity.Traits)
	}

	// Mutate and verify deep copy.
	got.Identity.Traits[0] = "mutated"
	refetched, _ := g.Node(node.ID)
	if refetched.Identity.Traits[0] != "t" {
		t.Fatalf("deep copy violated: got %q, want %q", refetched.Identity.Traits[0], "t")
	}
}

// --- copySleepContent ---

func TestCopySleepContent(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "sleep")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	sc := &SleepContent{Kind: SleepKindLight}
	node, _, err := g.AppendTypedNode(NodeKindSleep, Message{User: &UserMessage{}}, nil, sc, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	if got.Sleep == nil {
		t.Fatal("expected Sleep content, got nil")
	}
	if got.Sleep.Kind != SleepKindLight {
		t.Fatalf("Sleep.Kind = %q, want %q", got.Sleep.Kind, SleepKindLight)
	}
}

// --- copyAssistantMessage (ToolCalls + Refusal) ---

func TestCopyAssistantMessageToolCallsAndRefusal(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "asst")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	msg := Message{
		Assistant: &AssistantMessage{
			ToolCalls: []ToolCall{{ID: "tc1", Name: "fn", Arguments: "{}"}},
			Refusal:   "refused",
		},
	}
	node, _, err := g.AppendTypedNode(NodeKindExperience, msg, nil, nil, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	if got.Message.Assistant == nil {
		t.Fatal("expected Assistant message, got nil")
	}
	if got.Message.Assistant.Refusal != "refused" {
		t.Fatalf("Refusal = %q, want %q", got.Message.Assistant.Refusal, "refused")
	}
	if len(got.Message.Assistant.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(got.Message.Assistant.ToolCalls))
	}
	tc := got.Message.Assistant.ToolCalls[0]
	if tc.ID != "tc1" || tc.Name != "fn" || tc.Arguments != "{}" {
		t.Fatalf("ToolCall = %+v, unexpected", tc)
	}
}

// --- copyToolMessage ---

func TestCopyToolMessage(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "tool")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	msg := Message{
		Tool: &ToolMessage{
			ToolCallID: "tc1",
			Name:       "fn",
			Parts:      []ContentPart{{Text: &TextPart{Text: "result"}}},
			IsError:    true,
		},
	}
	node, _, err := g.AppendTypedNode(NodeKindExperience, msg, nil, nil, root.ID)
	if err != nil {
		t.Fatalf("AppendTypedNode: %v", err)
	}

	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}

	tm := got.Message.Tool
	if tm == nil {
		t.Fatal("expected Tool message, got nil")
	}
	if tm.ToolCallID != "tc1" || tm.Name != "fn" || !tm.IsError {
		t.Fatalf("ToolMessage = %+v, unexpected", tm)
	}
	if len(tm.Parts) != 1 || tm.Parts[0].Text == nil || tm.Parts[0].Text.Text != "result" {
		t.Fatalf("Parts = %+v, unexpected", tm.Parts)
	}
}

// --- copyContentPart + copyBlobPart ---

func TestCopyBlobPartsPreservedFields(t *testing.T) {
	t.Parallel()

	// Use Append (not AppendTypedNode) so copyMessage is called on store,
	// ensuring all ContentPart types are exercised through the copy path.
	data := []byte{1, 2, 3}

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "blob")))
	mustStart(t, g)
	mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	msg := Message{
		User: &UserMessage{
			Parts: []ContentPart{
				{Image: &ImagePart{BlobPart: BlobPart{MimeType: "image/png", Data: data}}},
				{Audio: &AudioPart{BlobPart: BlobPart{MimeType: "audio/mp3", Data: data}}},
				{Video: &VideoPart{BlobPart: BlobPart{MimeType: "video/mp4", Data: data}}},
				{File: &FilePart{BlobPart: BlobPart{MimeType: "text/plain", Data: data}, Name: "f.txt"}},
			},
		},
	}

	node, ev, err := g.Append(msg, "root")
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Verify all blob types are present on the returned node.
	parts := node.Message.User.Parts
	if len(parts) != 4 {
		t.Fatalf("parts len = %d, want 4", len(parts))
	}
	if parts[0].Image == nil || parts[0].Image.MimeType != "image/png" {
		t.Fatal("Image part not preserved")
	}
	if parts[1].Audio == nil || parts[1].Audio.MimeType != "audio/mp3" {
		t.Fatal("Audio part not preserved")
	}
	if parts[2].Video == nil || parts[2].Video.MimeType != "video/mp4" {
		t.Fatal("Video part not preserved")
	}
	if parts[3].File == nil || parts[3].File.Name != "f.txt" || parts[3].File.MimeType != "text/plain" {
		t.Fatal("File part not preserved")
	}

	// Verify the event also carries the node.
	if ev.Node == nil || ev.Node.ID != node.ID {
		t.Fatal("event node not set")
	}

	// Verify image data round-tripped correctly.
	if parts[0].Image.Data[0] != 1 {
		t.Fatalf("Image data[0] = %d, want 1", parts[0].Image.Data[0])
	}

	// Mutate original data; the stored copy must be unaffected (copyMessage is called on Append).
	data[0] = 99
	got, err := g.Node(node.ID)
	if err != nil {
		t.Fatalf("Node: %v", err)
	}
	if got.Message.User.Parts[0].Image.Data[0] != 1 {
		t.Fatal("deep copy violated: original slice mutation affected stored node")
	}
}

// --- WithIDGenerator nil safety ---

func TestWithIDGeneratorNilIsSafelyIgnored(t *testing.T) {
	t.Parallel()

	// Must not panic.
	g := NewGraph(WithIDGenerator(nil))
	ev := mustStart(t, g)
	if ev.SessionID == "" {
		t.Fatal("expected non-empty session id from default generator")
	}
}

func TestWithIDGeneratorCustomUsed(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("sid", "n1")))
	mustStart(t, g)
	node, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})
	if node.ID != "n1" {
		t.Fatalf("expected node id %q from custom generator, got %q", "n1", node.ID)
	}
}

// --- validateNodeReferences ---

func TestValidateNodeReferencesEmptyID(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root")))
	mustStart(t, g)
	mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	_, _, err := g.Append(Message{User: &UserMessage{}}, "")
	if err == nil {
		t.Fatal("expected error for empty parent ID, got nil")
	}
}

func TestValidateNodeReferencesNonexistentParent(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root")))
	mustStart(t, g)
	mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	_, _, err := g.Append(Message{User: &UserMessage{}}, "does-not-exist")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestValidateNodeReferencesDuplicateParentsDeduplicated(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "child")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	// Provide same parent ID twice.
	child, _, err := g.Append(Message{User: &UserMessage{}}, root.ID, root.ID)
	if err != nil {
		t.Fatalf("expected duplicate parents to be deduplicated, got %v", err)
	}

	// Should only have one parent edge.
	parents := child.Parents()
	if len(parents) != 1 {
		t.Fatalf("expected 1 parent edge after dedup, got %d", len(parents))
	}
}

// --- NeedsResponse extra cases ---

func TestNeedsResponseUnknownNodeID(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s")))
	mustStart(t, g)

	_, err := g.NeedsResponse("missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestNeedsResponseSystemLeafIsFalse(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "sys"}})

	needs, err := g.NeedsResponse(root.ID)
	if err != nil {
		t.Fatalf("NeedsResponse: %v", err)
	}
	if needs {
		t.Fatal("expected system leaf to not require response")
	}
}

func TestNeedsResponseAssistantLeafIsFalse(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "asst")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "sys"}})
	asst, _ := mustAppend(t, g, Message{Assistant: &AssistantMessage{}}, root.ID)

	needs, err := g.NeedsResponse(asst.ID)
	if err != nil {
		t.Fatalf("NeedsResponse: %v", err)
	}
	if needs {
		t.Fatal("expected assistant leaf to not require response")
	}
}

func TestNeedsResponseUserWithChildrenIsFalse(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "user", "asst")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "sys"}})
	user, _ := mustAppend(t, g, Message{User: &UserMessage{}}, root.ID)
	mustAppend(t, g, Message{Assistant: &AssistantMessage{}}, user.ID)

	needs, err := g.NeedsResponse(user.ID)
	if err != nil {
		t.Fatalf("NeedsResponse: %v", err)
	}
	if needs {
		t.Fatal("expected user with children to not require response")
	}
}

// --- ResumeNodes/ResumeMessages missing nodeID ---

func TestResumeNodesMissingID(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s")))
	mustStart(t, g)

	_, err := g.ResumeNodes("missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

func TestResumeMessagesMissingID(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s")))
	mustStart(t, g)

	_, err := g.ResumeMessages("missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

// --- AppendTypedNode all content types ---

func TestAppendTypedNodeAllKinds(t *testing.T) {
	t.Parallel()

	g := NewGraph(WithIDGenerator(sequenceIDs("s", "root", "mem", "skill", "ident", "sleep", "exp")))
	mustStart(t, g)
	root, _ := mustAppend(t, g, Message{System: &SystemMessage{Text: "root"}})

	// Memory
	memNode, memID, err := g.AppendTypedNode(
		NodeKindMemory,
		Message{User: &UserMessage{}},
		nil,
		&MemoryContent{Summary: "sum", Tags: []string{"t1"}, Importance: 0.5},
		root.ID,
	)
	if err != nil {
		t.Fatalf("AppendTypedNode memory: %v", err)
	}
	if memID == "" || memNode.Kind != NodeKindMemory || memNode.Memory == nil {
		t.Fatalf("memory node unexpected: %+v", memNode)
	}

	// Skill
	skillNode, skillID, err := g.AppendTypedNode(
		NodeKindSkill,
		Message{User: &UserMessage{}},
		nil,
		&SkillContent{Name: "sk", Description: "desc", Procedure: "proc"},
		root.ID,
	)
	if err != nil {
		t.Fatalf("AppendTypedNode skill: %v", err)
	}
	if skillID == "" || skillNode.Kind != NodeKindSkill || skillNode.Skill == nil {
		t.Fatalf("skill node unexpected: %+v", skillNode)
	}

	// Identity
	identNode, identID, err := g.AppendTypedNode(
		NodeKindIdentity,
		Message{User: &UserMessage{}},
		nil,
		&IdentityContent{Traits: []string{"brave"}, Values: []string{"honesty"}, Goals: []string{"grow"}},
		root.ID,
	)
	if err != nil {
		t.Fatalf("AppendTypedNode identity: %v", err)
	}
	if identID == "" || identNode.Kind != NodeKindIdentity || identNode.Identity == nil {
		t.Fatalf("identity node unexpected: %+v", identNode)
	}

	// Sleep
	sleepNode, sleepID, err := g.AppendTypedNode(
		NodeKindSleep,
		Message{User: &UserMessage{}},
		nil,
		&SleepContent{Kind: SleepKindREM},
		root.ID,
	)
	if err != nil {
		t.Fatalf("AppendTypedNode sleep: %v", err)
	}
	if sleepID == "" || sleepNode.Kind != NodeKindSleep || sleepNode.Sleep == nil {
		t.Fatalf("sleep node unexpected: %+v", sleepNode)
	}
	if sleepNode.Sleep.Kind != SleepKindREM {
		t.Fatalf("SleepKind = %q, want %q", sleepNode.Sleep.Kind, SleepKindREM)
	}

	// Experience (nil content) with extra edges
	extraEdge := EdgeRef{Kind: EdgeKindDistilledFrom, NodeID: memID}
	expNode, expID, err := g.AppendTypedNode(
		NodeKindExperience,
		Message{Assistant: &AssistantMessage{}},
		[]EdgeRef{extraEdge},
		nil,
		root.ID,
	)
	if err != nil {
		t.Fatalf("AppendTypedNode experience: %v", err)
	}
	if expID == "" || expNode.Kind != NodeKindExperience {
		t.Fatalf("experience node unexpected: %+v", expNode)
	}

	// Verify extra edge present
	foundExtra := false
	for _, e := range expNode.Edges {
		if e.Kind == EdgeKindDistilledFrom && e.NodeID == memID {
			foundExtra = true
		}
	}
	if !foundExtra {
		t.Fatalf("expected extra edge %+v in %+v", extraEdge, expNode.Edges)
	}
}
