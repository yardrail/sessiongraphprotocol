package sessiongraphprotocol

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrGraphNotFound indicates that a persisted graph could not be located.
	ErrGraphNotFound = errors.New("graph not found")
	// ErrNilGraph indicates that a nil graph was provided to a persistence API.
	ErrNilGraph = errors.New("graph is nil")
	// ErrInvalidSnapshot indicates that a graph snapshot cannot be restored.
	ErrInvalidSnapshot = errors.New("invalid graph snapshot")
)

const (
	// GraphSnapshotVersion1 is the first explicit snapshot schema version.
	GraphSnapshotVersion1 uint32 = 1
	// CurrentGraphSnapshotVersion is the version emitted by this package.
	CurrentGraphSnapshotVersion = GraphSnapshotVersion1
)

type snapshotUpgrader func(GraphSnapshot) (GraphSnapshot, error)

var snapshotUpgraders = map[uint32]snapshotUpgrader{}

// Store persists and loads SGP graphs.
type Store interface {
	Save(ctx context.Context, graph *Graph) error
	Load(ctx context.Context, sessionID ID) (*Graph, error)
}

// GraphSnapshot is the serializable representation of a graph.
type GraphSnapshot struct {
	Version        uint32     `json:"version"`
	Session        Session    `json:"session"`
	EventNames     EventNames `json:"event_names"`
	Nodes          []Node     `json:"nodes"`
	Events         []Event    `json:"events"`
	HeadID         ID         `json:"head_id,omitempty"`
	TerminalNodeID ID         `json:"terminal_node_id,omitempty"`
	Closed         bool       `json:"closed"`
}

// Snapshot returns a serializable copy of the graph.
func (graph *Graph) Snapshot() GraphSnapshot {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	return GraphSnapshot{
		Version:        CurrentGraphSnapshotVersion,
		Session:        Session{ID: graph.session.ID, Timestamp: graph.session.Timestamp, SpawnedFrom: copySpawnReference(graph.session.SpawnedFrom)},
		EventNames:     graph.eventNames,
		Nodes:          graph.snapshotNodes(),
		Events:         copyEvents(graph.events),
		HeadID:         graph.headID,
		TerminalNodeID: graph.terminalNodeID,
		Closed:         graph.closed,
	}
}

// RestoreGraph reconstructs an in-memory graph from a snapshot.
func RestoreGraph(snapshot GraphSnapshot) (*Graph, error) {
	upgradedSnapshot, err := UpgradeSnapshot(snapshot)
	if err != nil {
		return nil, err
	}

	snapshot = upgradedSnapshot

	if snapshot.Session.ID == "" {
		return nil, fmt.Errorf("%w: session id is required", ErrInvalidSnapshot)
	}

	eventNames := snapshot.EventNames
	if eventNames == (EventNames{}) {
		eventNames = DefaultEventNames()
	}

	graph := &Graph{
		session: Session{
			ID:          snapshot.Session.ID,
			Timestamp:   snapshot.Session.Timestamp,
			SpawnedFrom: copySpawnReference(snapshot.Session.SpawnedFrom),
		},
		eventNames: eventNames,
		clock: func() time.Time {
			return time.Now().UTC()
		},
		idGenerator: func() ID {
			return ID(uuid.NewString())
		},
		nodes:          make(map[ID]Node, len(snapshot.Nodes)),
		children:       make(map[ID][]ID),
		events:         copyEvents(snapshot.Events),
		headID:         snapshot.HeadID,
		terminalNodeID: snapshot.TerminalNodeID,
		closed:         snapshot.Closed,
	}

	for index := range graph.events {
		graph.events[index].Kind = eventKindFromName(eventNames, graph.events[index].Event)
	}

	for _, node := range snapshot.Nodes {
		if node.ID == "" {
			return nil, fmt.Errorf("%w: node id is required", ErrInvalidSnapshot)
		}

		if node.SessionID != graph.session.ID {
			return nil, fmt.Errorf("%w: node %s has session id %s", ErrInvalidSnapshot, node.ID, node.SessionID)
		}

		graph.nodes[node.ID] = copyNode(node)
	}

	for _, node := range snapshot.Nodes {
		for _, parentID := range node.ParentIDs {
			if _, exists := graph.nodes[parentID]; !exists {
				return nil, fmt.Errorf("%w: parent %s missing for node %s", ErrInvalidSnapshot, parentID, node.ID)
			}

			graph.children[parentID] = append(graph.children[parentID], node.ID)
		}

		for _, sourceID := range node.SynthesizedFrom {
			if _, exists := graph.nodes[sourceID]; !exists {
				return nil, fmt.Errorf("%w: synthesized source %s missing for node %s", ErrInvalidSnapshot, sourceID, node.ID)
			}
		}
	}

	if graph.headID != "" {
		if _, exists := graph.nodes[graph.headID]; !exists {
			return nil, fmt.Errorf("%w: head node %s missing", ErrInvalidSnapshot, graph.headID)
		}
	}

	if graph.terminalNodeID != "" {
		if _, exists := graph.nodes[graph.terminalNodeID]; !exists {
			return nil, fmt.Errorf("%w: terminal node %s missing", ErrInvalidSnapshot, graph.terminalNodeID)
		}
	}

	return graph, nil
}

// UpgradeSnapshot converts an older snapshot schema to the current version.
func UpgradeSnapshot(snapshot GraphSnapshot) (GraphSnapshot, error) {
	version := snapshot.Version
	if version == 0 {
		return GraphSnapshot{}, fmt.Errorf("%w: snapshot version is required", ErrInvalidSnapshot)
	}

	for version < CurrentGraphSnapshotVersion {
		upgrader, ok := snapshotUpgraders[version]
		if !ok {
			return GraphSnapshot{}, fmt.Errorf("%w: unsupported snapshot version %d", ErrInvalidSnapshot, version)
		}

		upgradedSnapshot, err := upgrader(snapshot)
		if err != nil {
			return GraphSnapshot{}, err
		}

		snapshot = upgradedSnapshot
		version = snapshot.Version
	}

	if version != CurrentGraphSnapshotVersion {
		return GraphSnapshot{}, fmt.Errorf("%w: unsupported snapshot version %d", ErrInvalidSnapshot, version)
	}

	return snapshot, nil
}

func (graph *Graph) snapshotNodes() []Node {
	nodeIDs := make([]ID, 0, len(graph.nodes))
	seen := make(map[ID]struct{}, len(graph.nodes))

	for _, event := range graph.events {
		if event.Node == nil {
			continue
		}

		if _, exists := seen[event.Node.ID]; exists {
			continue
		}

		seen[event.Node.ID] = struct{}{}
		nodeIDs = append(nodeIDs, event.Node.ID)
	}

	if len(nodeIDs) != len(graph.nodes) {
		missing := make([]string, 0, len(graph.nodes)-len(nodeIDs))
		for nodeID := range graph.nodes {
			if _, exists := seen[nodeID]; exists {
				continue
			}

			missing = append(missing, string(nodeID))
		}

		sort.Strings(missing)
		for _, nodeID := range missing {
			nodeIDs = append(nodeIDs, ID(nodeID))
		}
	}

	nodes := make([]Node, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		nodes = append(nodes, copyNode(graph.nodes[nodeID]))
	}

	return nodes
}

func eventKindFromName(eventNames EventNames, name string) EventKind {
	switch name {
	case eventNames.SessionStart:
		return EventKindSessionStart
	case eventNames.NodeAppended:
		return EventKindNodeAppended
	case eventNames.HistoryRewritten:
		return EventKindHistoryRewritten
	case eventNames.SessionEnded:
		return EventKindSessionEnded
	default:
		return 0
	}
}
