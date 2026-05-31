package liveagent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
	"google.golang.org/genai"
)

func TestHarnessHandleEventPersistsPerOACSession(t *testing.T) {
	t.Parallel()

	store, err := sgp.NewJSONFileStore(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatalf("NewJSONFileStore() error = %v", err)
	}

	harness, err := NewHarness(t.TempDir(), store, &stubModelGenerator{}, "test-model", "system")
	if err != nil {
		t.Fatalf("NewHarness() error = %v", err)
	}

	response, err := harness.HandleEvent(context.Background(), "oac-session-1", "agent.events", "text/plain", []byte("first request"))
	if err != nil {
		t.Fatalf("HandleEvent(first) error = %v", err)
	}
	if response != "ack 1: first request" {
		t.Fatalf("first response = %q, want ack 1: first request", response)
	}

	response, err = harness.HandleEvent(context.Background(), "oac-session-1", "agent.events", "application/json", []byte(`{"input":"second request"}`))
	if err != nil {
		t.Fatalf("HandleEvent(second) error = %v", err)
	}
	if response != "ack 2: second request" {
		t.Fatalf("second response = %q, want ack 2: second request", response)
	}

	graph, err := store.Load(context.Background(), sgp.ID("oac-session-1"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if graph.Session().ID != sgp.ID("oac-session-1") {
		t.Fatalf("session id = %s, want oac-session-1", graph.Session().ID)
	}

	snapshot := graph.Snapshot()
	if len(snapshot.Nodes) != 5 {
		t.Fatalf("node count = %d, want 5", len(snapshot.Nodes))
	}
	if snapshot.HeadID == "" {
		t.Fatal("head id is empty")
	}

	head, err := graph.Node(snapshot.HeadID)
	if err != nil {
		t.Fatalf("Node(head) error = %v", err)
	}
	if head.Message.Content != "ack 2: second request" {
		t.Fatalf("head content = %v, want ack 2: second request", head.Message.Content)
	}

	if err = harness.EndSession(context.Background(), "oac-session-1"); err != nil {
		t.Fatalf("EndSession() error = %v", err)
	}

	closedGraph, err := store.Load(context.Background(), sgp.ID("oac-session-1"))
	if err != nil {
		t.Fatalf("Load(closed) error = %v", err)
	}
	if !closedGraph.Snapshot().Closed {
		t.Fatal("closed snapshot flag = false, want true")
	}
}

type stubModelGenerator struct {
	callCount int
}

func (generator *stubModelGenerator) GenerateContent(_ context.Context, _ string, contents []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	generator.callCount++
	lastUser := ""
	for _, content := range contents {
		if content == nil || content.Role != string(genai.RoleUser) {
			continue
		}
		for _, part := range content.Parts {
			if part != nil && part.Text != "" {
				lastUser = part.Text
			}
		}
	}

	return synthesizeGenerateContentResponse(&ollamaActionEnvelope{
		Type:    "final",
		Content: fmt.Sprintf("ack %d: %s", generator.callCount, lastUser),
	}), nil
}
