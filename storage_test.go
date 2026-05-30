package sessiongraphprotocol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotUsesCurrentVersion(t *testing.T) {
	t.Parallel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))
	if _, _, err := graph.Append(Message{Role: MessageRoleSystem, Content: "sys"}, nil); err != nil {
		t.Fatalf("append root: %v", err)
	}

	snapshot := graph.Snapshot()
	if got, want := snapshot.Version, uint32(CurrentGraphSnapshotVersion); got != want {
		t.Fatalf("expected snapshot version %d, got %d", want, got)
	}
}

func TestUpgradeSnapshotAcceptsCurrentVersion(t *testing.T) {
	t.Parallel()

	snapshot := GraphSnapshot{
		Version: CurrentGraphSnapshotVersion,
		Session: Session{ID: "session-1"},
		Nodes: []Node{{
			ID:        "node-a",
			SessionID: "session-1",
			Message:   Message{Role: MessageRoleSystem, Content: "sys"},
		}},
		Events: []Event{{
			Event:     DefaultEventNames().SessionStart,
			SessionID: "session-1",
		}},
		HeadID: "node-a",
	}

	upgradedSnapshot, err := UpgradeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("upgrade snapshot: %v", err)
	}

	if got, want := upgradedSnapshot.Version, uint32(CurrentGraphSnapshotVersion); got != want {
		t.Fatalf("expected upgraded version %d, got %d", want, got)
	}

	if got, want := upgradedSnapshot.Version, snapshot.Version; got != want {
		t.Fatalf("expected upgraded version %d, got %d", want, got)
	}
}

func TestUpgradeSnapshotRejectsMissingVersion(t *testing.T) {
	t.Parallel()

	_, err := UpgradeSnapshot(GraphSnapshot{Session: Session{ID: "session-1"}})
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
	}
}

func TestUpgradeSnapshotRejectsFutureVersion(t *testing.T) {
	t.Parallel()

	_, err := UpgradeSnapshot(GraphSnapshot{Version: CurrentGraphSnapshotVersion + 1, Session: Session{ID: "session-1"}})
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
	}
}

func TestRestoreGraphWithCurrentVersionSnapshot(t *testing.T) {
	t.Parallel()

	snapshot := GraphSnapshot{
		Version: CurrentGraphSnapshotVersion,
		Session: Session{ID: "session-1"},
		Nodes: []Node{
			{ID: "node-a", SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}},
			{ID: "node-b", SessionID: "session-1", ParentIDs: []ID{"node-a"}, Message: Message{Role: MessageRoleUser, Content: "ask"}},
		},
		Events: []Event{
			{Event: DefaultEventNames().SessionStart, SessionID: "session-1"},
			{Event: DefaultEventNames().NodeAppended, SessionID: "session-1", Node: &Node{ID: "node-a", SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}}},
			{Event: DefaultEventNames().NodeAppended, SessionID: "session-1", Node: &Node{ID: "node-b", SessionID: "session-1", ParentIDs: []ID{"node-a"}, Message: Message{Role: MessageRoleUser, Content: "ask"}}},
		},
		HeadID: "node-b",
	}

	graph, err := RestoreGraph(snapshot)
	if err != nil {
		t.Fatalf("restore graph: %v", err)
	}

	messages, err := graph.ResumeMessages("node-b")
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if got, want := len(messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}

	events := graph.Events()
	if got, want := events[0].Kind, EventKindSessionStart; got != want {
		t.Fatalf("expected restored start kind %d, got %d", want, got)
	}
}

func TestRestoreGraphRejectsMissingVersion(t *testing.T) {
	t.Parallel()

	_, err := RestoreGraph(GraphSnapshot{Session: Session{ID: "session-1"}})
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
	}
}

func TestRestoreGraphRoundTripsSnapshot(t *testing.T) {
	t.Parallel()

	graph := NewGraph(
		WithIDGenerator(sequenceIDs("session-1", "node-a", "node-b")),
		WithEventNames(EventNames{
			SessionStart:     "sgp.session.started",
			NodeAppended:     "sgp.node.appended",
			HistoryRewritten: "sgp.history.rewritten",
			SessionEnded:     "sgp.session.ended",
		}),
	)

	root, _, err := graph.Append(Message{Role: MessageRoleSystem, Content: "sys"}, nil)
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	assistantNode, _, err := graph.Append(Message{Role: MessageRoleAssistant, Content: "answer"}, map[string]any{"provider": "openai"}, root.ID)
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	if _, err = graph.End(); err != nil {
		t.Fatalf("end graph: %v", err)
	}

	restored, err := RestoreGraph(graph.Snapshot())
	if err != nil {
		t.Fatalf("restore graph: %v", err)
	}

	messages, err := restored.ResumeMessages(assistantNode.ID)
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	events := restored.Events()
	if got, want := events[0].Kind, EventKindSessionStart; got != want {
		t.Fatalf("expected restored start kind %d, got %d", want, got)
	}

	if got, want := events[0].Event, "sgp.session.started"; got != want {
		t.Fatalf("expected restored custom event name %q, got %q", want, got)
	}

	if _, _, err = restored.Append(Message{Role: MessageRoleAssistant, Content: "late"}, nil, assistantNode.ID); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("expected restored closed graph to reject appends, got %v", err)
	}
}

func TestRestoreGraphRejectsInvalidSnapshots(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		snapshot GraphSnapshot
	}{
		{
			name:     "missing session id",
			snapshot: GraphSnapshot{Version: CurrentGraphSnapshotVersion},
		},
		{
			name: "missing node id",
			snapshot: GraphSnapshot{
				Version: CurrentGraphSnapshotVersion,
				Session: Session{ID: "session-1"},
				Nodes:   []Node{{SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}}},
			},
		},
		{
			name: "session mismatch",
			snapshot: GraphSnapshot{
				Version: CurrentGraphSnapshotVersion,
				Session: Session{ID: "session-1"},
				Nodes:   []Node{{ID: "node-a", SessionID: "session-2", Message: Message{Role: MessageRoleSystem, Content: "sys"}}},
			},
		},
		{
			name: "missing parent",
			snapshot: GraphSnapshot{
				Version: CurrentGraphSnapshotVersion,
				Session: Session{ID: "session-1"},
				Nodes:   []Node{{ID: "node-a", SessionID: "session-1", ParentIDs: []ID{"missing"}, Message: Message{Role: MessageRoleUser, Content: "sys"}}},
			},
		},
		{
			name: "missing synthesized source",
			snapshot: GraphSnapshot{
				Version: CurrentGraphSnapshotVersion,
				Session: Session{ID: "session-1"},
				Nodes: []Node{
					{ID: "node-a", SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}},
					{ID: "node-b", SessionID: "session-1", ParentIDs: []ID{"node-a"}, SynthesizedFrom: []ID{"missing"}, Message: Message{Role: MessageRoleAssistant, Content: "merged"}},
				},
			},
		},
		{
			name: "missing head",
			snapshot: GraphSnapshot{
				Version: CurrentGraphSnapshotVersion,
				Session: Session{ID: "session-1"},
				Nodes:   []Node{{ID: "node-a", SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}}},
				HeadID:  "missing",
			},
		},
		{
			name: "missing terminal",
			snapshot: GraphSnapshot{
				Version:        CurrentGraphSnapshotVersion,
				Session:        Session{ID: "session-1"},
				Nodes:          []Node{{ID: "node-a", SessionID: "session-1", Message: Message{Role: MessageRoleSystem, Content: "sys"}}},
				TerminalNodeID: "missing",
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := RestoreGraph(testCase.snapshot)
			if !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
			}
		})
	}
}

func TestNewJSONFileStoreRequiresBaseDir(t *testing.T) {
	t.Parallel()

	_, err := NewJSONFileStore("   ")
	if err == nil {
		t.Fatal("expected error for blank base dir")
	}
}

func TestJSONFileStoreSaveRejectsNilGraph(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	if err = store.Save(context.Background(), nil); !errors.Is(err, ErrNilGraph) {
		t.Fatalf("expected ErrNilGraph, got %v", err)
	}
}

func TestJSONFileStoreHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))
	if _, _, err = graph.Append(Message{Role: MessageRoleSystem, Content: "sys"}, nil); err != nil {
		t.Fatalf("append root: %v", err)
	}

	if err = store.Save(ctx, graph); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation from save, got %v", err)
	}

	if _, err = store.Load(ctx, graph.Session().ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation from load, got %v", err)
	}
}

func TestJSONFileStoreWritesVersionedJSON(t *testing.T) {
	t.Parallel()

	baseDir := filepath.Join(t.TempDir(), "graphs")
	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a")))
	if _, _, err = graph.Append(Message{Role: MessageRoleSystem, Content: "sys"}, nil); err != nil {
		t.Fatalf("append root: %v", err)
	}

	if err = store.Save(context.Background(), graph); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(baseDir, "session-1.json"))
	if err != nil {
		t.Fatalf("read saved graph: %v", err)
	}

	var snapshot GraphSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("unmarshal saved snapshot: %v", err)
	}

	if got, want := snapshot.Version, uint32(CurrentGraphSnapshotVersion); got != want {
		t.Fatalf("expected saved version %d, got %d", want, got)
	}
}

func TestJSONFileStoreRejectsSnapshotWithoutVersion(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	snapshotWithoutVersion := GraphSnapshot{
		Session: Session{ID: "legacy/session"},
		Nodes: []Node{{
			ID:        "node-a",
			SessionID: "legacy/session",
			Message:   Message{Role: MessageRoleSystem, Content: "sys"},
		}},
		Events: []Event{{Event: DefaultEventNames().SessionStart, SessionID: "legacy/session"}},
		HeadID: "node-a",
	}

	data, err := json.MarshalIndent(snapshotWithoutVersion, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot without version: %v", err)
	}

	if err = os.WriteFile(filepath.Join(baseDir, "legacy%2Fsession.json"), data, 0o644); err != nil {
		t.Fatalf("write snapshot without version: %v", err)
	}

	_, err = store.Load(context.Background(), "legacy/session")
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
	}
}

func TestJSONFileStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(filepath.Join(t.TempDir(), "graphs"))
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	graph := NewGraph(WithIDGenerator(sequenceIDs("session-1", "node-a", "node-b")))
	root, _, err := graph.Append(Message{Role: MessageRoleSystem, Content: "sys"}, nil)
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	userNode, _, err := graph.Append(Message{Role: MessageRoleUser, Content: "hello"}, nil, root.ID)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	ctx := context.Background()
	if err = store.Save(ctx, graph); err != nil {
		t.Fatalf("save graph: %v", err)
	}

	restored, err := store.Load(ctx, graph.Session().ID)
	if err != nil {
		t.Fatalf("load graph: %v", err)
	}

	needsResponse, err := restored.NeedsResponse(userNode.ID)
	if err != nil {
		t.Fatalf("needs response: %v", err)
	}

	if !needsResponse {
		t.Fatal("expected persisted dangling user node to still require a response")
	}

	messages, err := restored.ResumeMessages(userNode.ID)
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if got, want := len(messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
}

func TestJSONFileStoreMissingGraph(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	_, err = store.Load(context.Background(), "missing")
	if !errors.Is(err, ErrGraphNotFound) {
		t.Fatalf("expected ErrGraphNotFound, got %v", err)
	}
}

func TestJSONFileStoreRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	if err = os.WriteFile(filepath.Join(baseDir, "broken.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("write invalid json: %v", err)
	}

	_, err = store.Load(context.Background(), "broken")
	if err == nil {
		t.Fatal("expected invalid json load to fail")
	}
}

func TestJSONFileStoreRejectsInvalidSnapshotFile(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new json file store: %v", err)
	}

	invalidSnapshot := GraphSnapshot{
		Version: CurrentGraphSnapshotVersion,
		Session: Session{ID: "broken"},
		HeadID:  "missing",
	}

	data, err := json.Marshal(invalidSnapshot)
	if err != nil {
		t.Fatalf("marshal invalid snapshot: %v", err)
	}

	if err = os.WriteFile(filepath.Join(baseDir, "broken.json"), data, 0o644); err != nil {
		t.Fatalf("write invalid snapshot: %v", err)
	}

	_, err = store.Load(context.Background(), "broken")
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("expected ErrInvalidSnapshot, got %v", err)
	}
}
