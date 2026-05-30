package sessiongraphprotocol

import "time"

// ID is the string identifier used for sessions and nodes.
type ID string

// MessageRole identifies the role of a message in the inference transcript.
type MessageRole string

const (
	// MessageRoleSystem identifies a system message.
	MessageRoleSystem MessageRole = "system"
	// MessageRoleUser identifies a user message.
	MessageRoleUser MessageRole = "user"
	// MessageRoleAssistant identifies an assistant message.
	MessageRoleAssistant MessageRole = "assistant"
	// MessageRoleTool identifies a tool message.
	MessageRoleTool MessageRole = "tool"
)

// Message is the protocol payload stored on each node.
type Message struct {
	Role    MessageRole `json:"role"`
	Content any         `json:"content"`
}

// Node is an immutable session graph node.
type Node struct {
	ID              ID             `json:"id"`
	SessionID       ID             `json:"session_id"`
	Timestamp       time.Time      `json:"timestamp"`
	ParentIDs       []ID           `json:"parent_ids"`
	SynthesizedFrom []ID           `json:"synthesized_from,omitempty"`
	Message         Message        `json:"message"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// SpawnReference points to the parent session and node that created a subagent session.
type SpawnReference struct {
	SessionID ID `json:"session_id"`
	NodeID    ID `json:"node_id"`
}

// Session describes a single SGP session.
type Session struct {
	ID          ID              `json:"id"`
	Timestamp   time.Time       `json:"timestamp"`
	SpawnedFrom *SpawnReference `json:"spawned_from,omitempty"`
}

// EventKind is the stable enum used for session events.
type EventKind uint8

const (
	// EventKindSessionStart is emitted when a session starts.
	EventKindSessionStart EventKind = iota + 1
	// EventKindNodeAppended is emitted when a node is appended.
	EventKindNodeAppended
	// EventKindHistoryRewritten is emitted when a rewrite node is appended.
	EventKindHistoryRewritten
	// EventKindSessionEnded is emitted when a session ends.
	EventKindSessionEnded
)

// EventNames maps stable event kinds to emitted event strings.
type EventNames struct {
	SessionStart     string
	NodeAppended     string
	HistoryRewritten string
	SessionEnded     string
}

// DefaultEventNames returns the spec-defined event names.
func DefaultEventNames() EventNames {
	return EventNames{
		SessionStart:     "session.start",
		NodeAppended:     "node.appended",
		HistoryRewritten: "history.rewritten",
		SessionEnded:     "session.ended",
	}
}

// Name returns the configured event name for the provided kind.
func (names EventNames) Name(kind EventKind) string {
	switch kind {
	case EventKindSessionStart:
		return names.SessionStart
	case EventKindNodeAppended:
		return names.NodeAppended
	case EventKindHistoryRewritten:
		return names.HistoryRewritten
	case EventKindSessionEnded:
		return names.SessionEnded
	default:
		return ""
	}
}

// Event is the emitted session event envelope.
type Event struct {
	Kind           EventKind       `json:"-"`
	Event          string          `json:"event"`
	SessionID      ID              `json:"session_id,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`
	SpawnedFrom    *SpawnReference `json:"spawned_from,omitempty"`
	Node           *Node           `json:"node,omitempty"`
	TerminalNodeID ID              `json:"terminal_node_id,omitempty"`
}