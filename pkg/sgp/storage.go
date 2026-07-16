package sgp

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

var (
	// ErrGraphNotFound indicates that a persisted graph could not be located.
	ErrGraphNotFound = errors.New("graph not found")

	// ErrSessionNotFound indicates that the requested session does not exist in the store.
	ErrSessionNotFound = errors.New("session not found")

	errUnexpectedSessionStart = errors.New("unexpected second session.start event")
	errMissingNode            = errors.New("missing node")
	errNodeIDRequired         = errors.New("node id is required")
	errNodeSessionIDMismatch  = errors.New("node has wrong session id")
	errMissingSessionStart    = errors.New("event log missing session.start event")
	errCycleDetected          = errors.New("cycle detected in session node graph")
)

// Store persists and retrieves SGP session data.
//
// CreateSession records a new session. WriteNode appends a node.
// EndSession marks a session closed. LoadGraph reconstructs an in-memory [Graph]
// from stored data.
//
// The Store interface makes no concurrency guarantees for concurrent writes to
// the same session. Callers are responsible for serialising concurrent writes
// targeting the same sessionID.
type Store interface {
	CreateSession(ctx context.Context, sess Session) error
	WriteNode(ctx context.Context, node Node) error
	EndSession(ctx context.Context, sessionID ID, reason EndReason, terminalNodeID ID) error

	LoadGraph(ctx context.Context, sessionID ID) (*Graph, error)
	GetNode(ctx context.Context, nodeID ID) (Node, error)
	GetLineage(ctx context.Context, nodeID ID) ([]Node, error)
	GetSession(ctx context.Context, sessionID ID) (Session, SessionStatus, error)
	ListSessions(ctx context.Context, cursor string, limit int) ([]Session, string, error)
}

// Watcher is an optional interface implemented by stores with push notification
// support. Callers type-assert: if w, ok := store.(sgp.Watcher); ok { ... }
//
// Watch subscribes to live node writes for sessionID. The returned channel
// receives each Node written after the subscription is established. The cancel
// func unsubscribes and closes the channel. Watch returns [ErrSessionNotFound]
// if the session does not exist.
type Watcher interface {
	Watch(ctx context.Context, sessionID ID) (<-chan Node, func(), error)
}

// ClassifyEvent determines the EventKind for an event using field presence.
// It is robust to custom EventNames because it never compares event name strings.
// Store implementations should call ClassifyEvent to restore Event.Kind on events
// loaded from persistent storage (Kind is not serialised).
func ClassifyEvent(event Event) EventKind {
	if event.Node != nil {
		return EventKindNodeAppended
	}

	if event.Reason != "" || event.TerminalNodeID != "" {
		return EventKindSessionEnded
	}

	return EventKindSessionStart
}

// RestoreFromEvents reconstructs an in-memory [Graph] from a persisted event log.
// events must be ordered by emission time, as returned by a store's LoadEvents call.
//
// EventNames are inferred from the event name strings observed in the log; any
// kind not represented falls back to [DefaultEventNames]. The restored graph uses
// the inferred names for all future [Graph.Append] and [Graph.End] calls,
// preserving custom event name configuration across restarts.
//
// Returns [ErrGraphNotFound] if events is empty.
func RestoreFromEvents(events []Event) (*Graph, error) {
	if len(events) == 0 {
		return nil, ErrGraphNotFound
	}

	eventNames := DefaultEventNames()

	graph := &Graph{
		nodes:    make(map[ID]Node),
		children: make(map[ID][]ID),
		idGenerator: func() ID {
			return ID(uuid.NewString())
		},
	}

	for i, event := range events {
		kind := ClassifyEvent(event)
		event.Kind = kind

		err := applyEventToGraph(graph, &eventNames, i, event, kind)
		if err != nil {
			return nil, err
		}

		graph.events = append(graph.events, copyEvent(event))
	}

	if !graph.started {
		return nil, errMissingSessionStart
	}

	graph.eventNames = eventNames

	return graph, nil
}

// RestoreFromNodes reconstructs an in-memory [Graph] from a flat slice of nodes,
// session metadata, and terminal state. It is used by stores that persist quads
// or rows directly (e.g. Cayley) rather than event logs.
//
// nodes may arrive in any order; RestoreFromNodes topologically sorts them via
// Kahn's algorithm before wiring the graph. Returns an error if a cycle is
// detected or if a parent reference is missing.
//
// The restored graph's events slice is empty; event replay is not required.
func RestoreFromNodes(
	sess Session,
	nodes []Node,
	headID ID,
	status SessionStatus,
	reason EndReason,
	terminalNodeID ID,
) (*Graph, error) {
	nodeMap := make(map[ID]Node, len(nodes))
	for _, n := range nodes {
		nodeMap[n.ID] = n
	}

	sorted, err := topoSortNodes(nodeMap)
	if err != nil {
		return nil, err
	}

	graph := &Graph{
		session: sess,
		started: true,
		nodes:   make(map[ID]Node, len(nodes)),
		children: make(map[ID][]ID, len(nodes)),
		headID:  headID,
		idGenerator: func() ID {
			return ID(uuid.NewString())
		},
		eventNames: DefaultEventNames(),
	}

	for _, n := range sorted {
		graph.nodes[n.ID] = n
		for _, parentID := range n.Parents() {
			graph.children[parentID] = append(graph.children[parentID], n.ID)
		}
	}

	if status == SessionStatusClosed {
		graph.closed = true
		graph.endReason = reason
		graph.terminalNodeID = terminalNodeID
	}

	return graph, nil
}

// SynthesizeEvents reconstructs an ordered event slice from a Graph.
// It is used by store backends that do not natively persist events (e.g. Cayley)
// to produce an event log for the LoadEvents RPC wire format.
func SynthesizeEvents(g *Graph) []Event {
	g.mu.RLock()
	defer g.mu.RUnlock()

	names := g.eventNames
	if names.SessionStart == "" {
		names = DefaultEventNames()
	}

	events := make([]Event, 0, len(g.nodes)+2)

	events = append(events, Event{
		Kind:        EventKindSessionStart,
		Event:       names.SessionStart,
		SessionID:   g.session.ID,
		Timestamp:   g.session.Timestamp,
		SpawnedFrom: copySpawnReference(g.session.SpawnedFrom),
	})

	sorted, _ := topoSortNodes(g.nodes)
	for _, node := range sorted {
		n := copyNode(node)
		events = append(events, Event{
			Kind:      EventKindNodeAppended,
			Event:     names.NodeAppended,
			SessionID: g.session.ID,
			Timestamp: node.Timestamp,
			Node:      &n,
		})
	}

	if g.closed {
		events = append(events, Event{
			Kind:           EventKindSessionEnded,
			Event:          names.SessionEnded,
			SessionID:      g.session.ID,
			Timestamp:      g.session.Timestamp,
			TerminalNodeID: g.terminalNodeID,
			Reason:         g.endReason,
		})
	}

	return events
}

// nodeDependencyIDs returns all node IDs that must be sorted before n.
// Includes both EdgeKindParent and EdgeKindBranchFrom edges, since a branch
// node must sort after its origin even though it is not a canonical parent.
func nodeDependencyIDs(n Node) []ID {
	var ids []ID
	for _, e := range n.Edges {
		if e.Kind == EdgeKindParent || e.Kind == EdgeKindBranchFrom {
			ids = append(ids, e.NodeID)
		}
	}
	return ids
}

// topoSortNodes topologically sorts a node map via Kahn's algorithm.
// Parent edges (via Parents()) are treated as ordering edges.
// Within the same topological level, nodes are ordered by timestamp.
func topoSortNodes(nodeMap map[ID]Node) ([]Node, error) {
	inDegree := make(map[ID]int, len(nodeMap))
	edgesFrom := make(map[ID][]ID, len(nodeMap))

	for id := range nodeMap {
		inDegree[id] = 0
	}

	for _, n := range nodeMap {
		refs := nodeDependencyIDs(n)
		seen := make(map[ID]struct{}, len(refs))

		for _, ref := range refs {
			if _, dup := seen[ref]; dup {
				continue
			}
			if _, ok := nodeMap[ref]; !ok {
				continue // ref outside session scope
			}
			seen[ref] = struct{}{}
			inDegree[n.ID]++
			edgesFrom[ref] = append(edgesFrom[ref], n.ID)
		}
	}

	queue := make([]ID, 0, len(nodeMap))
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	sort.Slice(queue, func(i, j int) bool {
		return nodeMap[queue[i]].Timestamp.Before(nodeMap[queue[j]].Timestamp)
	})

	sorted := make([]Node, 0, len(nodeMap))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, nodeMap[id])

		children := edgesFrom[id]
		sort.Slice(children, func(i, j int) bool {
			return nodeMap[children[i]].Timestamp.Before(nodeMap[children[j]].Timestamp)
		})
		for _, childID := range children {
			inDegree[childID]--
			if inDegree[childID] == 0 {
				queue = append(queue, childID)
			}
		}
	}

	if len(sorted) != len(nodeMap) {
		return nil, errCycleDetected
	}

	return sorted, nil
}

// applyEventToGraph applies a single classified event to the graph being restored.
func applyEventToGraph(
	graph *Graph,
	eventNames *EventNames,
	i int,
	event Event,
	kind EventKind,
) error {
	switch kind {
	case EventKindSessionStart:
		return applySessionStartEvent(graph, eventNames, i, event)
	case EventKindNodeAppended:
		return applyNodeEvent(graph, eventNames, i, event)
	case EventKindSessionEnded:
		graph.closed = true
		graph.terminalNodeID = event.TerminalNodeID
		graph.endReason = event.Reason
		eventNames.SessionEnded = event.Event
	default:
		// Unknown event kind; ignore.
	}

	return nil
}

// applySessionStartEvent handles a session.start event during restore.
func applySessionStartEvent(graph *Graph, eventNames *EventNames, i int, event Event) error {
	if graph.started {
		return fmt.Errorf("event at index %d: %w", i, errUnexpectedSessionStart)
	}

	graph.session.ID = event.SessionID

	graph.session.Timestamp = event.Timestamp
	graph.session.SpawnedFrom = copySpawnReference(event.SpawnedFrom)
	graph.started = true

	eventNames.SessionStart = event.Event

	return nil
}

// applyNodeEvent handles a node.appended event during restore.
func applyNodeEvent(
	graph *Graph,
	eventNames *EventNames,
	i int,
	event Event,
) error {
	if event.Node == nil {
		return fmt.Errorf("event at index %d: %w", i, errMissingNode)
	}

	node := copyNode(*event.Node)

	if node.ID == "" {
		return fmt.Errorf("event at index %d: %w", i, errNodeIDRequired)
	}

	if node.SessionID == "" || node.SessionID != graph.session.ID {
		return fmt.Errorf(
			"event at index %d: %w: node %s has session id %q, expected %q",
			i,
			errNodeSessionIDMismatch,
			node.ID,
			node.SessionID,
			graph.session.ID,
		)
	}

	err := linkNodeParents(graph, i, node)
	if err != nil {
		return err
	}

	graph.nodes[node.ID] = node
	graph.headID = node.ID
	eventNames.NodeAppended = event.Event

	return nil
}

// linkNodeParents wires parent→child edges for a node being restored.
func linkNodeParents(graph *Graph, i int, node Node) error {
	for _, parentID := range node.Parents() {
		if _, exists := graph.nodes[parentID]; !exists {
			return fmt.Errorf(
				"event at index %d: %w: parent %s missing for node %s",
				i,
				ErrNodeNotFound,
				parentID,
				node.ID,
			)
		}

		graph.children[parentID] = append(graph.children[parentID], node.ID)
	}

	return nil
}
