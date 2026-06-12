package sgpsession

import (
	"context"
	"os"
	"testing"
	"time"

	sgp "github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	jsonstore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/json"
	"google.golang.org/adk/session"
)

func TestServiceCreateAppendAndPersistSGP(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()

	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	created, err := svc.Create(ctx, &session.CreateRequest{
		AppName: "app",
		UserID:  "user",
		State: map[string]any{
			"app:region":  "us-east-1",
			"user:locale": "en-US",
			"mode":        "interactive",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	event := &session.Event{
		ID:           "event-1",
		Author:       "user",
		InvocationID: "inv-1",
		Timestamp:    time.Now().UTC(),
		Actions: session.EventActions{
			StateDelta: map[string]any{
				"app:region":  "us-west-2",
				"user:locale": "fr-FR",
				"mode":        "batch",
				"temp:cache":  "discard-me",
			},
		},
	}
	if err = svc.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	got, err := svc.Get(ctx, &session.GetRequest{
		AppName:   "app",
		UserID:    "user",
		SessionID: created.Session.ID(),
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got.Session.State() == nil {
		t.Fatalf("Get().Session.State() is nil")
	}

	stateVal, err := got.Session.State().Get("mode")
	if err != nil {
		t.Fatalf("state.Get(mode) error = %v", err)
	}
	if stateVal != "batch" {
		t.Fatalf("state mode = %v, want batch", stateVal)
	}

	if got.Session.Events().Len() != 1 {
		t.Fatalf("events len = %d, want 1", got.Session.Events().Len())
	}
	storedEvent := got.Session.Events().At(0)
	if _, ok := storedEvent.Actions.StateDelta["temp:cache"]; ok {
		t.Fatalf("temp state key should be trimmed from stored event")
	}

	store, err := jsonstore.NewJSONFileStore(dir)
	if err != nil {
		t.Fatalf("NewJSONFileStore() error = %v", err)
	}
	graph, err := store.Load(ctx, sgp.ID(created.Session.ID()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	snapshot := graph.Snapshot()
	if len(snapshot.Nodes) != 1 {
		t.Fatalf("SGP node count = %d, want 1", len(snapshot.Nodes))
	}
	head, ok := graph.Head()
	if !ok {
		t.Fatalf("graph head missing")
	}
	if head.Message.Role() != sgp.MessageRoleUser {
		t.Fatalf("head role = %s, want user", head.Message.Role())
	}
}

func TestServiceListAndRecentEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, err := NewService(t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	s1, err := svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "u1"})
	if err != nil {
		t.Fatalf("Create(s1) error = %v", err)
	}
	_, err = svc.Create(ctx, &session.CreateRequest{AppName: "app", UserID: "u2"})
	if err != nil {
		t.Fatalf("Create(s2) error = %v", err)
	}

	for i := range 3 {
		err = svc.AppendEvent(ctx, s1.Session, &session.Event{
			ID:        "event-" + string(rune('a'+i)),
			Author:    "assistant",
			Timestamp: time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("AppendEvent(%d) error = %v", i, err)
		}
	}

	list, err := svc.List(ctx, &session.ListRequest{AppName: "app", UserID: "u1"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("list len = %d, want 1", len(list.Sessions))
	}

	recent, err := svc.Get(ctx, &session.GetRequest{
		AppName:         "app",
		UserID:          "u1",
		SessionID:       s1.Session.ID(),
		NumRecentEvents: 2,
	})
	if err != nil {
		t.Fatalf("Get(recent) error = %v", err)
	}
	if recent.Session.Events().Len() != 2 {
		t.Fatalf("recent events len = %d, want 2", recent.Session.Events().Len())
	}
}

func TestServiceDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	created, err := svc.Create(
		ctx,
		&session.CreateRequest{AppName: "app", UserID: "u1", SessionID: "s1"},
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if err = svc.Delete(ctx, &session.DeleteRequest{AppName: "app", UserID: "u1", SessionID: created.Session.ID()}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = svc.Get(ctx, &session.GetRequest{AppName: "app", UserID: "u1", SessionID: "s1"})
	if err == nil {
		t.Fatalf("Get() expected error after delete")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected SGP snapshot file to remain for audit history")
	}
}

func TestServiceIngestOrchestratorEvent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	svc, err := NewService(dir)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	err = svc.IngestOrchestratorEvent(
		ctx,
		"oac_sgp_agent",
		"orchestrator",
		"2af00f80-36a5-4130-a509-486d06fdf47f",
		"agent.events",
		"application/json",
		[]byte(`{"ok":true}`),
	)
	if err != nil {
		t.Fatalf("IngestOrchestratorEvent() error = %v", err)
	}

	store, err := jsonstore.NewJSONFileStore(dir)
	if err != nil {
		t.Fatalf("NewJSONFileStore() error = %v", err)
	}
	graph, err := store.Load(ctx, sgp.ID("2af00f80-36a5-4130-a509-486d06fdf47f"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	head, ok := graph.Head()
	if !ok {
		t.Fatalf("graph head missing")
	}
	if head.Message.Role() != sgp.MessageRoleTool {
		t.Fatalf("head role = %s, want tool", head.Message.Role())
	}
	if head.Message.Tool == nil || head.Message.Tool.Name != "agent.events" {
		name := ""
		if head.Message.Tool != nil {
			name = head.Message.Tool.Name
		}
		t.Fatalf("oac_channel = %v, want agent.events", name)
	}
}
