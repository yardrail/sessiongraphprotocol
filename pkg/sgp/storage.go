package sgp

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

var (
	// ErrGraphNotFound indicates that a persisted graph could not be located.
	ErrGraphNotFound = errors.New("graph not found")

	errUnexpectedSessionStart = errors.New("unexpected second session.start event")
	errMissingNode            = errors.New("missing node")
	errNodeIDRequired         = errors.New("node id is required")
	errNodeSessionIDMismatch  = errors.New("node has wrong session id")
	errMissingSessionStart    = errors.New("event log missing session.start event")
)

// Store persists SGP session events as an append-only log.
//
// AppendEvent appends a single event to the named session's log. Implementations
// must return [ErrGraphNotFound] from LoadEvents when no events have been recorded
// for the given session ID.
//
// The Store interface makes no concurrency guarantees for writes to the same
// session. Callers are responsible for serialising concurrent AppendEvent calls
// targeting the same sessionID.
//
// Implementations must restore the [Event.Kind] field on events returned by
// LoadEvents using [ClassifyEvent].
type Store interface {
	AppendEvent(ctx context.Context, sessionID ID, event Event) error
	LoadEvents(ctx context.Context, sessionID ID) ([]Event, error)
}

// ClassifyEvent determines the EventKind for an event using field presence.
// It is robust to custom EventNames because it never compares event name strings.
// Store implementations should call ClassifyEvent to restore Event.Kind on events
// loaded from persistent storage (Kind is not serialised).
func ClassifyEvent(event Event) EventKind {
	if event.Node != nil {
		if len(event.Node.SynthesizedFrom) > 0 {
			return EventKindHistoryRewritten
		}

		return EventKindNodeAppended
	}

	if event.Reason != "" || event.TerminalNodeID != "" {
		return EventKindSessionEnded
	}

	return EventKindSessionStart
}

// RestoreFromEvents reconstructs an in-memory [Graph] from a persisted event log.
// events must be ordered by emission time, as returned by [Store.LoadEvents].
//
// EventNames are inferred from the event name strings observed in the log; any
// kind not represented falls back to [DefaultEventNames]. The restored graph uses
// the inferred names for all future [Graph.Append], [Graph.Rewrite], and
// [Graph.End] calls, preserving custom event name configuration across restarts.
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
	case EventKindNodeAppended, EventKindHistoryRewritten:
		return applyNodeEvent(graph, eventNames, i, event, kind)
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

// applyNodeEvent handles a node.appended or history.rewritten event during restore.
func applyNodeEvent(
	graph *Graph,
	eventNames *EventNames,
	i int,
	event Event,
	kind EventKind,
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

	err = checkSynthesizedSources(graph, i, node)
	if err != nil {
		return err
	}

	graph.nodes[node.ID] = node

	graph.headID = node.ID

	if kind == EventKindNodeAppended {
		eventNames.NodeAppended = event.Event
	} else {
		eventNames.HistoryRewritten = event.Event
	}

	return nil
}

// linkNodeParents wires parent→child edges for a node being restored.
func linkNodeParents(graph *Graph, i int, node Node) error {
	for _, parentID := range node.ParentIDs {
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

// checkSynthesizedSources verifies all synthesized-from references exist.
func checkSynthesizedSources(graph *Graph, i int, node Node) error {
	for _, sourceID := range node.SynthesizedFrom {
		if _, exists := graph.nodes[sourceID]; !exists {
			return fmt.Errorf(
				"event at index %d: %w: synthesized source %s missing for node %s",
				i,
				ErrNodeNotFound,
				sourceID,
				node.ID,
			)
		}
	}

	return nil
}
