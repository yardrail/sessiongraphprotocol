package sgp

import (
	"strings"
	"time"
)

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

// BlobPart carries embedded binary content with a MIME type.
type BlobPart struct {
	MimeType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

// TextPart carries plain or markdown text.
type TextPart struct {
	Text string `json:"text"`
}

// ImagePart carries an embedded image.
type ImagePart struct{ BlobPart }

// AudioPart carries embedded audio.
type AudioPart struct{ BlobPart }

// VideoPart carries embedded video.
type VideoPart struct{ BlobPart }

// FilePart carries an embedded file, optionally named.
type FilePart struct {
	BlobPart

	Name string `json:"name,omitempty"`
}

// ContentPart is a discriminated union of message content types.
// Exactly one field must be non-nil.
type ContentPart struct {
	Text  *TextPart  `json:"text,omitempty"`
	Image *ImagePart `json:"image,omitempty"`
	Audio *AudioPart `json:"audio,omitempty"`
	Video *VideoPart `json:"video,omitempty"`
	File  *FilePart  `json:"file,omitempty"`
}

// ToolCall describes a model-requested tool invocation, mirroring the OpenAI inference API.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// SystemMessage carries a system prompt.
type SystemMessage struct {
	Text string `json:"text"`
}

// UserMessage carries a user turn, optionally with multi-modal content.
type UserMessage struct {
	Parts []ContentPart `json:"parts"`
}

// AssistantMessage carries a model inference response, mirroring the OpenAI inference API.
// Parts carries text output; ToolCalls carries tool invocations requested by the model;
// Refusal carries a model refusal when the model declines to respond.
type AssistantMessage struct {
	Parts     []ContentPart `json:"parts,omitempty"`
	ToolCalls []ToolCall    `json:"tool_calls,omitempty"`
	Refusal   string        `json:"refusal,omitempty"`
}

// ToolMessage carries a tool result, modeled after MCP's CallToolResult.
// ToolCallID correlates the result back to the originating AssistantMessage ToolCall.
type ToolMessage struct {
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name"`
	Parts      []ContentPart `json:"parts"`
	IsError    bool          `json:"is_error,omitempty"`
}

// Message is a discriminated union of session message subtypes.
// Exactly one field must be non-nil.
type Message struct {
	System    *SystemMessage    `json:"system,omitempty"`
	User      *UserMessage      `json:"user,omitempty"`
	Assistant *AssistantMessage `json:"assistant,omitempty"`
	Tool      *ToolMessage      `json:"tool,omitempty"`
}

// Role returns the message role derived from which subtype is set.
func (m Message) Role() MessageRole {
	switch {
	case m.System != nil:
		return MessageRoleSystem
	case m.User != nil:
		return MessageRoleUser
	case m.Assistant != nil:
		return MessageRoleAssistant
	case m.Tool != nil:
		return MessageRoleTool
	default:
		return ""
	}
}

// TextContent returns the concatenated text from all TextParts in the message.
func (m Message) TextContent() string {
	switch {
	case m.System != nil:
		return m.System.Text
	case m.User != nil:
		return contentPartsText(m.User.Parts)
	case m.Assistant != nil:
		return contentPartsText(m.Assistant.Parts)
	case m.Tool != nil:
		return contentPartsText(m.Tool.Parts)
	default:
		return ""
	}
}

func contentPartsText(parts []ContentPart) string {
	var sb strings.Builder

	for _, part := range parts {
		if part.Text != nil {
			sb.WriteString(part.Text.Text)
		}
	}

	return sb.String()
}

// valid reports whether exactly one message subtype is set.
func (m Message) valid() bool {
	count := 0

	if m.System != nil {
		count++
	}

	if m.User != nil {
		count++
	}

	if m.Assistant != nil {
		count++
	}

	if m.Tool != nil {
		count++
	}

	return count == 1
}

// Node is an immutable session graph node.
type Node struct {
	ID              ID        `json:"id"`
	SessionID       ID        `json:"session_id"`
	Timestamp       time.Time `json:"timestamp"`
	ParentIDs       []ID      `json:"parent_ids"`
	SynthesizedFrom []ID      `json:"synthesized_from,omitempty"`
	Message         Message   `json:"message"`
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

// SessionStatus indicates whether a session is open or closed.
type SessionStatus int

const (
	// SessionStatusOpen indicates the session is active and accepting new nodes.
	SessionStatusOpen SessionStatus = 1
	// SessionStatusClosed indicates the session has ended.
	SessionStatusClosed SessionStatus = 2
)

// EndReason describes why a session terminated. It is carried on the
// session.ended event and persisted in the graph snapshot.
type EndReason string

const (
	// EndReasonComplete indicates the session finished successfully.
	EndReasonComplete EndReason = "complete"
	// EndReasonFailed indicates the session terminated due to an error.
	EndReasonFailed EndReason = "failed"
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
	Reason         EndReason       `json:"reason,omitempty"`
}
