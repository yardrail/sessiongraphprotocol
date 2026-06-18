package cayleystore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cayleygraph/cayley/graph"
	_ "github.com/cayleygraph/cayley/graph/memstore"
	cayleystore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/cayley"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

func newTestStore(t *testing.T) *cayleystore.Store {
	t.Helper()
	qs, err := graph.NewQuadStore("memstore", "", nil)
	if err != nil {
		t.Fatalf("new quad store: %v", err)
	}
	t.Cleanup(func() { qs.Close() })
	return cayleystore.New(qs)
}

func makeSession(id string) sgp.Session {
	return sgp.Session{ID: sgp.ID(id), Timestamp: time.Now().UTC()}
}

func makeNode(id, sessionID string, parentIDs ...sgp.ID) sgp.Node {
	return sgp.Node{
		ID:        sgp.ID(id),
		SessionID: sgp.ID(sessionID),
		Timestamp: time.Now().UTC(),
		ParentIDs: parentIDs,
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "test"}},
	}
}

func TestCreateSessionGetSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, status, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != "s1" {
		t.Fatalf("expected session id s1, got %s", got.ID)
	}
	if status != sgp.SessionStatusOpen {
		t.Fatalf("expected open status, got %d", status)
	}
}

func TestWriteNodeGetNode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	node := makeNode("n1", "s1")
	node.Message = sgp.Message{User: &sgp.UserMessage{Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "hello"}}}}}

	if err := store.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	got, err := store.GetNode(ctx, "n1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.ID != "n1" {
		t.Fatalf("expected node id n1, got %s", got.ID)
	}
	if got.Message.User == nil || got.Message.User.Parts[0].Text.Text != "hello" {
		t.Fatalf("message not preserved correctly")
	}
}

func TestLoadGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	if err := store.WriteNode(ctx, n1); err != nil {
		t.Fatalf("WriteNode n1: %v", err)
	}
	n2 := makeNode("n2", "s1", "n1")
	if err := store.WriteNode(ctx, n2); err != nil {
		t.Fatalf("WriteNode n2: %v", err)
	}

	g, err := store.LoadGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	head, ok := g.Head()
	if !ok {
		t.Fatal("expected graph to have a head node")
	}
	if head.ID != "n2" {
		t.Fatalf("expected head n2, got %s", head.ID)
	}

	if g.Session().ID != "s1" {
		t.Fatalf("expected session s1, got %s", g.Session().ID)
	}
}

func TestGetLineageLinear(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	n2 := makeNode("n2", "s1", "n1")
	n3 := makeNode("n3", "s1", "n2")
	for _, n := range []sgp.Node{n1, n2, n3} {
		if err := store.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	lineage, err := store.GetLineage(ctx, "n3")
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	if len(lineage) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(lineage))
	}
	if lineage[0].ID != "n1" || lineage[2].ID != "n3" {
		t.Fatalf("unexpected lineage order: %v", lineage)
	}
}

func TestGetLineageRewrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	n2 := makeNode("n2", "s1", "n1")
	// branch nodes
	b1 := makeNode("b1", "s1", "n2")
	b2 := makeNode("b2", "s1", "n2")
	// rewrite: canonical parent=n2, synthesized_from=b1,b2
	rewrite := sgp.Node{
		ID: "rw", SessionID: "s1", Timestamp: time.Now().UTC(),
		ParentIDs:       []sgp.ID{"n2"},
		SynthesizedFrom: []sgp.ID{"b1", "b2"},
		Message:         sgp.Message{Assistant: &sgp.AssistantMessage{}},
	}

	for _, n := range []sgp.Node{n1, n2, b1, b2, rewrite} {
		if err := store.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	lineage, err := store.GetLineage(ctx, "rw")
	if err != nil {
		t.Fatalf("GetLineage: %v", err)
	}
	// Canonical path: n1 -> n2 -> rw (3 nodes, branches excluded)
	if len(lineage) != 3 {
		t.Fatalf("expected 3 nodes in canonical lineage, got %d", len(lineage))
	}
	if lineage[2].ID != "rw" {
		t.Fatalf("expected rw at end, got %s", lineage[2].ID)
	}
}

func TestListSessionsCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	base := time.Now().UTC()
	for i, id := range []string{"s1", "s2", "s3"} {
		sess := sgp.Session{ID: sgp.ID(id), Timestamp: base.Add(time.Duration(i) * time.Second)}
		if err := store.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
	}

	// First page: limit 2
	page1, next, err := store.ListSessions(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(page1))
	}
	if next == "" {
		t.Fatal("expected next cursor")
	}

	// Second page
	page2, next2, err := store.ListSessions(ctx, next, 2)
	if err != nil {
		t.Fatalf("ListSessions page2: %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("expected 1 session on page2, got %d", len(page2))
	}
	if next2 != "" {
		t.Fatalf("expected no more pages, got %q", next2)
	}
}

func TestEndSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	if err := store.WriteNode(ctx, n1); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	if err := store.EndSession(ctx, "s1", sgp.EndReasonComplete, "n1"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	_, status, err := store.GetSession(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSession after end: %v", err)
	}
	if status != sgp.SessionStatusClosed {
		t.Fatalf("expected closed status, got %d", status)
	}
}

func TestWatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	ch, cancel, err := store.Watch(ctx, "s1")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer cancel()

	node := makeNode("n1", "s1")
	if err := store.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	select {
	case got := <-ch:
		if got.ID != "n1" {
			t.Fatalf("expected n1, got %s", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for node on Watch channel")
	}
}

func TestWriteNodeMissingSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	node := makeNode("n1", "nonexistent")
	err := store.WriteNode(ctx, node)
	if err == nil {
		t.Fatal("expected error for missing session, got nil")
	}
	if !errors.Is(err, sgp.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}
