package jsonstore //nolint:revive // package name differs from directory intentionally

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

func sequenceIDs(ids ...sgp.ID) sgp.IDGenerator {
	index := 0

	return func() sgp.ID {
		if index >= len(ids) {
			panic("sequenceIDs exhausted")
		}

		id := ids[index]
		index++

		return id
	}
}

func TestNewJSONFileStoreRequiresBaseDir(t *testing.T) {
	t.Parallel()

	_, err := NewJSONFileStore("   ")
	if err == nil {
		t.Fatal("expected error for blank base dir")
	}
}

func TestJSONFileStoreLoadEventsNotFound(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, err = store.LoadEvents(context.Background(), "no-such-session")
	if !errors.Is(err, sgp.ErrGraphNotFound) {
		t.Fatalf("expected ErrGraphNotFound, got %v", err)
	}
}

func TestJSONFileStoreHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1", "n1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	err = store.AppendEvent(ctx, graph.Session().ID, startEvent)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from AppendEvent, got %v", err)
	}

	_, err = store.LoadEvents(ctx, graph.Session().ID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled from LoadEvents, got %v", err)
	}
}

func mustAppendEvents(t *testing.T, store *JSONFileStore, sessionID sgp.ID, events []sgp.Event) {
	t.Helper()

	ctx := context.Background()

	for _, event := range events {
		err := store.AppendEvent(ctx, sessionID, event)
		if err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
}

func TestJSONFileStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(filepath.Join(t.TempDir(), "graphs"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1", "n-sys", "n-user")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, appendSys, err := graph.Append(sgp.Message{System: &sgp.SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	userNode, appendUser, err := graph.Append(
		sgp.Message{
			User: &sgp.UserMessage{Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "hello"}}}},
		},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	sessionID := graph.Session().ID
	mustAppendEvents(t, store, sessionID, []sgp.Event{startEvent, appendSys, appendUser})

	events, err := store.LoadEvents(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}

	restored, err := sgp.RestoreFromEvents(events)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	assertRestoredRoundTrip(t, restored, userNode.ID)
}

func assertRestoredRoundTrip(t *testing.T, g *sgp.Graph, userNodeID sgp.ID) {
	t.Helper()

	needsResponse, err := g.NeedsResponse(userNodeID)
	if err != nil {
		t.Fatalf("needs response: %v", err)
	}

	if !needsResponse {
		t.Fatal("expected dangling user node to require a response after restore")
	}

	messages, err := g.ResumeMessages(userNodeID)
	if err != nil {
		t.Fatalf("resume messages: %v", err)
	}

	if got, want := len(messages), 2; got != want {
		t.Fatalf("expected %d messages, got %d", want, got)
	}
}

func TestJSONFileStoreEventsLoadedInAppendOrder(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1", "n1", "n2", "n3")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, e1, err := graph.Append(sgp.Message{System: &sgp.SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append root: %v", err)
	}

	_, e2, err := graph.Append(
		sgp.Message{
			User: &sgp.UserMessage{Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "q"}}}},
		},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}

	_, e3, err := graph.Append(
		sgp.Message{
			Assistant: &sgp.AssistantMessage{
				Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "a"}}},
			},
		},
		root.ID,
	)
	if err != nil {
		t.Fatalf("append asst: %v", err)
	}

	sessionID := graph.Session().ID
	mustAppendEvents(t, store, sessionID, []sgp.Event{startEvent, e1, e2, e3})

	events, err := store.LoadEvents(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}

	if got, want := len(events), 4; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}

	if got, want := events[0].Kind, sgp.EventKindSessionStart; got != want {
		t.Fatalf("event[0] expected kind %d, got %d", want, got)
	}

	if got, want := events[1].Kind, sgp.EventKindNodeAppended; got != want {
		t.Fatalf("event[1] expected kind %d, got %d", want, got)
	}
}

func TestJSONFileStoreKindRestoredOnLoad(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1", "n1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	root, nodeEvent, err := graph.Append(sgp.Message{System: &sgp.SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	endEvent, err := graph.End(sgp.EndReasonComplete)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	_ = root

	sessionID := graph.Session().ID
	mustAppendEvents(t, store, sessionID, []sgp.Event{startEvent, nodeEvent, endEvent})

	events, err := store.LoadEvents(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}

	kinds := make([]sgp.EventKind, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}

	expected := []sgp.EventKind{
		sgp.EventKindSessionStart,
		sgp.EventKindNodeAppended,
		sgp.EventKindSessionEnded,
	}

	for i, want := range expected {
		if got := kinds[i]; got != want {
			t.Fatalf("event[%d] kind: expected %d, got %d", i, want, got)
		}
	}
}

func TestJSONFileStoreEndReasonPreservedOnLoad(t *testing.T) {
	t.Parallel()

	store, err := NewJSONFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1", "n1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	_, _, err = graph.Append(sgp.Message{System: &sgp.SystemMessage{Text: "sys"}})
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	endEvent, err := graph.End(sgp.EndReasonFailed)
	if err != nil {
		t.Fatalf("end: %v", err)
	}

	sessionID := graph.Session().ID
	mustAppendEvents(t, store, sessionID, []sgp.Event{startEvent, endEvent})

	events, err := store.LoadEvents(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("load events: %v", err)
	}

	var foundReason sgp.EndReason

	for _, e := range events {
		if e.Kind == sgp.EventKindSessionEnded {
			foundReason = e.Reason
		}
	}

	if got, want := foundReason, sgp.EndReasonFailed; got != want {
		t.Fatalf("expected end reason %q, got %q", want, got)
	}
}

func TestJSONFileStoreInvalidJSONLineReturnsError(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()

	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	// Write a valid first line then corrupt the second.
	sessionID := sgp.ID("broken-session")
	validLine := `{"event":"session.start","session_id":"broken-session","timestamp":"2025-01-01T00:00:00Z"}`
	content := validLine + "\n{not-json}\n"

	err = os.WriteFile(
		filepath.Join(baseDir, "broken-session.jsonl"),
		[]byte(content),
		0o600,
	)
	if err != nil {
		t.Fatalf("write test file: %v", err)
	}

	_, err = store.LoadEvents(context.Background(), sessionID)
	if err == nil {
		t.Fatal("expected error for invalid JSON line, got nil")
	}
}

func TestJSONFileStoreCreatesBaseDirOnFirstAppend(t *testing.T) {
	t.Parallel()

	baseDir := filepath.Join(t.TempDir(), "nested", "graphs")

	store, err := NewJSONFileStore(baseDir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	graph := sgp.NewGraph(sgp.WithIDGenerator(sequenceIDs("s1")))

	startEvent, err := graph.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	err = store.AppendEvent(context.Background(), graph.Session().ID, startEvent)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	_, err = os.Stat(baseDir)
	if err != nil {
		t.Fatalf("expected base dir to be created, got %v", err)
	}
}
