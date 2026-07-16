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
	edges := make([]sgp.EdgeRef, 0, len(parentIDs))
	for _, pid := range parentIDs {
		edges = append(edges, sgp.EdgeRef{Kind: sgp.EdgeKindParent, NodeID: pid})
	}
	return sgp.Node{
		ID:        sgp.ID(id),
		SessionID: sgp.ID(sessionID),
		Timestamp: time.Now().UTC(),
		Edges:     edges,
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

func TestWriteMemoryNode(t *testing.T) {
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

	mem := sgp.Node{
		ID:        "m1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind: sgp.NodeKindMemory,
		Edges: []sgp.EdgeRef{
			{Kind: sgp.EdgeKindParent, NodeID: "n1"},
			{Kind: sgp.EdgeKindDistilledFrom, NodeID: "n1"},
		},
		Memory: &sgp.MemoryContent{
			Summary:    "the first node",
			Tags:       []string{"test"},
			Importance: 0.8,
		},
		Message: sgp.Message{System: &sgp.SystemMessage{Text: "memory"}},
	}
	if err := store.WriteNode(ctx, mem); err != nil {
		t.Fatalf("WriteNode memory: %v", err)
	}

	got, err := store.GetNode(ctx, "m1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Kind != sgp.NodeKindMemory {
		t.Fatalf("expected kind %q, got %q", sgp.NodeKindMemory, got.Kind)
	}
	if got.Memory == nil {
		t.Fatal("expected Memory content, got nil")
	}
	if got.Memory.Summary != "the first node" {
		t.Fatalf("expected summary %q, got %q", "the first node", got.Memory.Summary)
	}
	if got.Memory.Importance != 0.8 {
		t.Fatalf("expected importance 0.8, got %v", got.Memory.Importance)
	}
	var hasDistilledFrom bool
	for _, e := range got.Edges {
		if e.Kind == sgp.EdgeKindDistilledFrom && e.NodeID == "n1" {
			hasDistilledFrom = true
		}
	}
	if !hasDistilledFrom {
		t.Fatal("expected distilled_from edge to n1")
	}
}

func TestWeightedEdgeRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newTestStore(t)

	sess := makeSession("s1")
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	m1 := sgp.Node{
		ID:        "m1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Memory:    &sgp.MemoryContent{Summary: "first memory", Importance: 0.9},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "m1"}},
	}
	m2 := sgp.Node{
		ID:        "m2",
		SessionID: "s1",
		Timestamp: time.Now().Add(time.Millisecond).UTC(),
		Kind:      sgp.NodeKindMemory,
		Edges: []sgp.EdgeRef{
			{Kind: sgp.EdgeKindAssociatedWith, NodeID: "m1", Weight: 0.87},
		},
		Memory:  &sgp.MemoryContent{Summary: "second memory", Importance: 0.7},
		Message: sgp.Message{System: &sgp.SystemMessage{Text: "m2"}},
	}

	for _, n := range []sgp.Node{m1, m2} {
		if err := store.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	got, err := store.GetNode(ctx, "m2")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	var assocEdge *sgp.EdgeRef
	for i, e := range got.Edges {
		if e.Kind == sgp.EdgeKindAssociatedWith && e.NodeID == "m1" {
			assocEdge = &got.Edges[i]
			break
		}
	}
	if assocEdge == nil {
		t.Fatal("expected associated_with edge to m1, got none")
	}
	if assocEdge.Weight != 0.87 {
		t.Fatalf("expected weight 0.87, got %v", assocEdge.Weight)
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

func TestBranchFromEdgeRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	sess := sgp.Session{ID: "s1", Timestamp: time.Now().UTC()}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	origin := makeNode("origin", "s1")
	if err := s.WriteNode(ctx, origin); err != nil {
		t.Fatalf("WriteNode origin: %v", err)
	}

	branch := sgp.Node{
		ID:        "branch",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindBranchFrom, NodeID: "origin"}},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "branch start"}},
	}
	if err := s.WriteNode(ctx, branch); err != nil {
		t.Fatalf("WriteNode branch: %v", err)
	}

	got, err := s.GetNode(ctx, "branch")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(got.Edges) != 1 || got.Edges[0].Kind != sgp.EdgeKindBranchFrom || got.Edges[0].NodeID != "origin" {
		t.Fatalf("edges: got %v, want [{branch_from origin}]", got.Edges)
	}
}

// ── GetSessionGraph ──────────────────────────────────────────────────────────

func TestGetSessionGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	n2 := makeNode("n2", "s1", "n1")
	n3 := makeNode("n3", "s1", "n2")
	n4 := sgp.Node{
		ID:        "n4",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindDistilledFrom, NodeID: "n1"}},
		Memory:    &sgp.MemoryContent{Summary: "distilled", Importance: 0.5},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "mem"}},
	}
	for _, n := range []sgp.Node{n1, n2, n3, n4} {
		if err := s.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	nodes, edges, err := s.GetSessionGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSessionGraph: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(nodes))
	}
	if len(edges) == 0 {
		t.Fatal("expected edges, got none")
	}

	// Verify n4 distilled_from edge is present.
	var found bool
	for _, e := range edges {
		if e.FromID == "n4" && e.ToID == "n1" && e.Kind == "distilled_from" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected distilled_from edge n4->n1 in edges %v", edges)
	}
}

// ── All EdgeKind roundtrips (edgeKindToPred coverage) ────────────────────────

func TestAllEdgeKindsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	anchor := makeNode("anchor", "s1")
	if err := s.WriteNode(ctx, anchor); err != nil {
		t.Fatalf("WriteNode anchor: %v", err)
	}

	node := sgp.Node{
		ID:        "all-edges",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Memory:    &sgp.MemoryContent{Summary: "all edges", Importance: 0.1},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "all-edges"}},
		Edges: []sgp.EdgeRef{
			{Kind: sgp.EdgeKindDistilledFrom, NodeID: "anchor"},
			{Kind: sgp.EdgeKindAssociatedWith, NodeID: "anchor", Weight: 0.5},
			{Kind: sgp.EdgeKindRecalledIn, NodeID: "anchor", Weight: 0.3},
			{Kind: sgp.EdgeKindEvolvedFrom, NodeID: "anchor"},
			{Kind: sgp.EdgeKindProceduralOf, NodeID: "anchor"},
			{Kind: sgp.EdgeKindArchives, NodeID: "anchor"},
			{Kind: sgp.EdgeKindBranchFrom, NodeID: "anchor"},
		},
	}
	if err := s.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode all-edges: %v", err)
	}

	got, err := s.GetNode(ctx, "all-edges")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}

	wantKinds := map[sgp.EdgeKind]bool{
		sgp.EdgeKindDistilledFrom:  false,
		sgp.EdgeKindAssociatedWith: false,
		sgp.EdgeKindRecalledIn:     false,
		sgp.EdgeKindEvolvedFrom:    false,
		sgp.EdgeKindProceduralOf:   false,
		sgp.EdgeKindArchives:       false,
		sgp.EdgeKindBranchFrom:     false,
	}
	for _, e := range got.Edges {
		wantKinds[e.Kind] = true
	}
	for k, seen := range wantKinds {
		if !seen {
			t.Errorf("missing edge kind %q in roundtrip", k)
		}
	}
}

// ── marshalNodeContent / GetNode content types ───────────────────────────────

func TestGetNodeWithSkillContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	node := sgp.Node{
		ID:        "skill1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindSkill,
		Skill: &sgp.SkillContent{
			Name:        "cooking",
			Description: "how to cook",
			Procedure:   "step 1: buy food",
		},
		Message: sgp.Message{System: &sgp.SystemMessage{Text: "skill"}},
	}
	if err := s.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	got, err := s.GetNode(ctx, "skill1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Skill == nil {
		t.Fatal("expected Skill content, got nil")
	}
	if got.Skill.Name != "cooking" {
		t.Fatalf("expected name 'cooking', got %q", got.Skill.Name)
	}
	if got.Skill.Procedure != "step 1: buy food" {
		t.Fatalf("expected procedure mismatch, got %q", got.Skill.Procedure)
	}
}

func TestGetNodeWithIdentityContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	node := sgp.Node{
		ID:        "id1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindIdentity,
		Identity: &sgp.IdentityContent{
			Traits: []string{"curious", "helpful"},
			Values: []string{"honesty"},
			Goals:  []string{"assist users"},
		},
		Message: sgp.Message{System: &sgp.SystemMessage{Text: "identity"}},
	}
	if err := s.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	got, err := s.GetNode(ctx, "id1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Identity == nil {
		t.Fatal("expected Identity content, got nil")
	}
	if len(got.Identity.Traits) != 2 || got.Identity.Traits[0] != "curious" {
		t.Fatalf("identity traits mismatch: %v", got.Identity.Traits)
	}
}

func TestGetNodeWithSleepContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	node := sgp.Node{
		ID:        "sleep1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindSleep,
		Sleep:     &sgp.SleepContent{Kind: sgp.SleepKindREM},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "sleep"}},
	}
	if err := s.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	got, err := s.GetNode(ctx, "sleep1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Sleep == nil {
		t.Fatal("expected Sleep content, got nil")
	}
	if got.Sleep.Kind != sgp.SleepKindREM {
		t.Fatalf("expected REM sleep, got %q", got.Sleep.Kind)
	}
}

// ── sessionToDeltas SpawnedFrom branch ───────────────────────────────────────

func TestCreateSessionWithSpawnedFrom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	parent := makeSession("parent-sess")
	if err := s.CreateSession(ctx, parent); err != nil {
		t.Fatalf("CreateSession parent: %v", err)
	}

	parentNode := makeNode("parent-node", "parent-sess")
	if err := s.WriteNode(ctx, parentNode); err != nil {
		t.Fatalf("WriteNode parent-node: %v", err)
	}

	child := sgp.Session{
		ID:        "child-sess",
		Timestamp: time.Now().UTC(),
		SpawnedFrom: &sgp.SpawnReference{
			SessionID: "parent-sess",
			NodeID:    "parent-node",
		},
	}
	if err := s.CreateSession(ctx, child); err != nil {
		t.Fatalf("CreateSession child: %v", err)
	}

	got, _, err := s.GetSession(ctx, "child-sess")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.SpawnedFrom == nil {
		t.Fatal("expected SpawnedFrom, got nil")
	}
	if got.SpawnedFrom.SessionID != "parent-sess" {
		t.Fatalf("expected parent-sess, got %q", got.SpawnedFrom.SessionID)
	}
	if got.SpawnedFrom.NodeID != "parent-node" {
		t.Fatalf("expected parent-node, got %q", got.SpawnedFrom.NodeID)
	}
}

// ── LoadGraph edge cases ──────────────────────────────────────────────────────

func TestLoadGraphNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.LoadGraph(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session, got nil")
	}
}

func TestLoadGraphClosedSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	if err := s.WriteNode(ctx, n1); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	if err := s.EndSession(ctx, "s1", sgp.EndReasonComplete, "n1"); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	g, err := s.LoadGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadGraph: %v", err)
	}

	// A restored closed graph should reject further appends with ErrSessionClosed.
	_, _, appendErr := g.Append(sgp.Message{User: &sgp.UserMessage{}}, "n1")
	if !errors.Is(appendErr, sgp.ErrSessionClosed) {
		t.Fatalf("expected ErrSessionClosed from closed graph, got %v", appendErr)
	}
}

// ── ListSessions edge cases ───────────────────────────────────────────────────

func TestListSessionsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	sessions, next, err := s.ListSessions(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
	if next != "" {
		t.Fatalf("expected no cursor, got %q", next)
	}
}

func TestListSessionsDefaultLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	base := time.Now().UTC()
	for i := range 3 {
		sess := sgp.Session{ID: sgp.ID("sx" + string(rune('0'+i))), Timestamp: base.Add(time.Duration(i) * time.Second)}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	// limit=0 should default to 50, returning all 3
	sessions, next, err := s.ListSessions(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}
	if next != "" {
		t.Fatalf("expected no cursor, got %q", next)
	}
}

// ── WriteNode head updates ────────────────────────────────────────────────────

func TestWriteNodeHeadUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	n1 := makeNode("n1", "s1")
	if err := s.WriteNode(ctx, n1); err != nil {
		t.Fatalf("WriteNode n1: %v", err)
	}

	g1, err := s.LoadGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadGraph after n1: %v", err)
	}
	head1, ok := g1.Head()
	if !ok || head1.ID != "n1" {
		t.Fatalf("expected head n1, got ok=%v id=%v", ok, head1.ID)
	}

	n2 := makeNode("n2", "s1", "n1")
	if err := s.WriteNode(ctx, n2); err != nil {
		t.Fatalf("WriteNode n2: %v", err)
	}

	g2, err := s.LoadGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadGraph after n2: %v", err)
	}
	head2, ok := g2.Head()
	if !ok || head2.ID != "n2" {
		t.Fatalf("expected head n2, got ok=%v id=%v", ok, head2.ID)
	}

	n3 := makeNode("n3", "s1", "n2")
	if err := s.WriteNode(ctx, n3); err != nil {
		t.Fatalf("WriteNode n3: %v", err)
	}

	g3, err := s.LoadGraph(ctx, "s1")
	if err != nil {
		t.Fatalf("LoadGraph after n3: %v", err)
	}
	head3, ok := g3.Head()
	if !ok || head3.ID != "n3" {
		t.Fatalf("expected head n3, got ok=%v id=%v", ok, head3.ID)
	}
}

// ── parseRFC3339 fallback path ────────────────────────────────────────────────

func TestParseRFC3339FallbackViaSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	// Use a timestamp with only second precision (no sub-second part).
	// RFC3339Nano will fail to parse it, RFC3339 will succeed.
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	sess := sgp.Session{ID: "s-rfc", Timestamp: ts}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, _, err := s.GetSession(ctx, "s-rfc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("timestamp mismatch: want %v got %v", ts, got.Timestamp)
	}
}

// ── Unweighted AssociatedWith / RecalledIn edge (weight=0) ───────────────────

func TestUnweightedAssociatedWithEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	m1 := sgp.Node{
		ID:        "m1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Memory:    &sgp.MemoryContent{Summary: "mem1", Importance: 0.5},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "m1"}},
	}
	m2 := sgp.Node{
		ID:        "m2",
		SessionID: "s1",
		Timestamp: time.Now().Add(time.Millisecond).UTC(),
		Kind:      sgp.NodeKindMemory,
		// Weight=0 means unweighted path in nodeToDeltas
		Edges:   []sgp.EdgeRef{{Kind: sgp.EdgeKindAssociatedWith, NodeID: "m1", Weight: 0}},
		Memory:  &sgp.MemoryContent{Summary: "mem2", Importance: 0.4},
		Message: sgp.Message{System: &sgp.SystemMessage{Text: "m2"}},
	}
	for _, n := range []sgp.Node{m1, m2} {
		if err := s.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	got, err := s.GetNode(ctx, "m2")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	var found bool
	for _, e := range got.Edges {
		if e.Kind == sgp.EdgeKindAssociatedWith && e.NodeID == "m1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected associated_with edge to m1")
	}
}

// ── EvolvedFrom / ProceduralOf edges ────────────────────────────────────────

func TestEvolvedFromEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	v1 := sgp.Node{
		ID:        "v1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Memory:    &sgp.MemoryContent{Summary: "v1"},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "v1"}},
	}
	v2 := sgp.Node{
		ID:        "v2",
		SessionID: "s1",
		Timestamp: time.Now().Add(time.Millisecond).UTC(),
		Kind:      sgp.NodeKindMemory,
		Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindEvolvedFrom, NodeID: "v1"}},
		Memory:    &sgp.MemoryContent{Summary: "v2"},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "v2"}},
	}
	for _, n := range []sgp.Node{v1, v2} {
		if err := s.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	got, err := s.GetNode(ctx, "v2")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	var found bool
	for _, e := range got.Edges {
		if e.Kind == sgp.EdgeKindEvolvedFrom && e.NodeID == "v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected evolved_from edge to v1")
	}
}

// ── Archived node roundtrip (nodeToDeltas archived branch) ───────────────────

func TestArchivedNodeRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	node := sgp.Node{
		ID:        "arch1",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Archived:  true,
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "archived"}},
	}
	if err := s.WriteNode(ctx, node); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	got, err := s.GetNode(ctx, "arch1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if !got.Archived {
		t.Fatal("expected Archived=true, got false")
	}
}

// ── GetNode not found ─────────────────────────────────────────────────────────

func TestGetNodeNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	_, err := s.GetNode(ctx, "no-such-node")
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
	if !errors.Is(err, sgp.ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got %v", err)
	}
}

// ── GetLineage error path (parent node missing) ───────────────────────────────

func TestGetLineageBrokenParent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Write a node that references a parent that was never written.
	// GetLineage will try to fetch the ghost parent and fail.
	ghost := sgp.Node{
		ID:        "ghost-child",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindParent, NodeID: "ghost-parent"}},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "orphan"}},
	}
	if err := s.WriteNode(ctx, ghost); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	_, err := s.GetLineage(ctx, "ghost-child")
	if err == nil {
		t.Fatal("expected error when parent node missing, got nil")
	}
}

func TestProceduralOfEdge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateSession(ctx, makeSession("s1")); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	src := sgp.Node{
		ID:        "src",
		SessionID: "s1",
		Timestamp: time.Now().UTC(),
		Kind:      sgp.NodeKindMemory,
		Memory:    &sgp.MemoryContent{Summary: "source"},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "src"}},
	}
	skill := sgp.Node{
		ID:        "sk1",
		SessionID: "s1",
		Timestamp: time.Now().Add(time.Millisecond).UTC(),
		Kind:      sgp.NodeKindSkill,
		Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindProceduralOf, NodeID: "src"}},
		Skill:     &sgp.SkillContent{Name: "skill1", Description: "desc", Procedure: "do it"},
		Message:   sgp.Message{System: &sgp.SystemMessage{Text: "sk1"}},
	}
	for _, n := range []sgp.Node{src, skill} {
		if err := s.WriteNode(ctx, n); err != nil {
			t.Fatalf("WriteNode %s: %v", n.ID, err)
		}
	}

	got, err := s.GetNode(ctx, "sk1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	var found bool
	for _, e := range got.Edges {
		if e.Kind == sgp.EdgeKindProceduralOf && e.NodeID == "src" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected procedural_of edge to src")
	}
}
