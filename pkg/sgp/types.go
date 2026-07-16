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

// NodeKind identifies the semantic type of a session graph node.
type NodeKind string

const (
	// NodeKindExperience is the default kind — a live inference turn.
	NodeKindExperience NodeKind = "experience"
	// NodeKindMemory is a distilled semantic memory node.
	NodeKindMemory NodeKind = "memory"
	// NodeKindSkill is a learned procedural skill node.
	NodeKindSkill NodeKind = "skill"
	// NodeKindIdentity is a persistent agent identity node.
	NodeKindIdentity NodeKind = "identity"
	// NodeKindSleep marks a sleep cycle boundary.
	NodeKindSleep NodeKind = "sleep"
)

// EdgeKind identifies the semantic relationship between two nodes.
type EdgeKind string

const (
	// EdgeKindParent is the default parent-child ordering edge.
	EdgeKindParent EdgeKind = "parent"
	// EdgeKindDistilledFrom links a memory node to the experience nodes it summarises.
	EdgeKindDistilledFrom EdgeKind = "distilled_from"
	// EdgeKindAssociatedWith is a weighted semantic association between memory nodes.
	EdgeKindAssociatedWith EdgeKind = "associated_with"
	// EdgeKindRecalledIn links a memory node to the experience node that recalled it.
	EdgeKindRecalledIn EdgeKind = "recalled_in"
	// EdgeKindEvolvedFrom links an updated node to its predecessor.
	EdgeKindEvolvedFrom EdgeKind = "evolved_from"
	// EdgeKindProceduralOf links a skill node to the memory or experience that produced it.
	EdgeKindProceduralOf EdgeKind = "procedural_of"
	// EdgeKindArchives links a sleep node to the experience nodes it archives.
	EdgeKindArchives EdgeKind = "archives"
	// EdgeKindBranchFrom marks a node that begins a deliberate branch of the session
	// (e.g. a sub-agent fork). The node links to its origin via this edge instead of
	// EdgeKindParent, so it does not appear in the children map and AdvanceHead skips it.
	EdgeKindBranchFrom EdgeKind = "branch_from"
)

// EdgeRef is a typed, optionally weighted reference from one node to another.
type EdgeRef struct {
	Kind   EdgeKind `json:"kind"`
	NodeID ID       `json:"node_id"`
	Weight float64  `json:"weight,omitempty"` // 0 = unweighted
}

// MemoryContent holds the structured payload of a NodeKindMemory node.
type MemoryContent struct {
	Summary    string   `json:"summary"`
	Tags       []string `json:"tags,omitempty"`
	Importance float64  `json:"importance"`
}

// SkillContent holds the structured payload of a NodeKindSkill node.
type SkillContent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Procedure   string `json:"procedure"`
}

// SleepKind identifies the depth of a sleep cycle.
type SleepKind string

const (
	// SleepKindLight is a light-sleep cycle that distils experiences into memories.
	SleepKindLight SleepKind = "light"
	// SleepKindREM is a REM sleep cycle that forms weighted associations between memories.
	SleepKindREM SleepKind = "rem"
)

// SleepContent holds the structured payload of a NodeKindSleep node.
type SleepContent struct {
	Kind SleepKind `json:"kind"`
}

// IdentityContent holds the structured payload of a NodeKindIdentity node.
type IdentityContent struct {
	Traits []string `json:"traits,omitempty"`
	Values []string `json:"values,omitempty"`
	Goals  []string `json:"goals,omitempty"`
}

// Node is an immutable session graph node.
type Node struct {
	ID              ID        `json:"id"`
	SessionID       ID        `json:"session_id"`
	Timestamp       time.Time `json:"timestamp"`
	Message         Message   `json:"message"`
	// new fields
	Kind     NodeKind  `json:"kind,omitempty"`
	Edges    []EdgeRef `json:"edges,omitempty"`
	Archived bool      `json:"archived,omitempty"`
	// content (at most one non-nil)
	Memory   *MemoryContent   `json:"memory,omitempty"`
	Skill    *SkillContent    `json:"skill,omitempty"`
	Identity *IdentityContent `json:"identity,omitempty"`
	Sleep    *SleepContent    `json:"sleep,omitempty"`
}

// Parents returns parent node IDs from Edges.
func (n Node) Parents() []ID {
	var ids []ID
	for _, e := range n.Edges {
		if e.Kind == EdgeKindParent {
			ids = append(ids, e.NodeID)
		}
	}
	return ids
}

// EffectiveKind returns NodeKindExperience if Kind is empty (backward compat).
func (n Node) EffectiveKind() NodeKind {
	if n.Kind == "" {
		return NodeKindExperience
	}
	return n.Kind
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
	SessionStart string
	NodeAppended string
	SessionEnded string
}

// DefaultEventNames returns the spec-defined event names.
func DefaultEventNames() EventNames {
	return EventNames{
		SessionStart: "session.start",
		NodeAppended: "node.appended",
		SessionEnded: "session.ended",
	}
}

// Name returns the configured event name for the provided kind.
func (names EventNames) Name(kind EventKind) string {
	switch kind {
	case EventKindSessionStart:
		return names.SessionStart
	case EventKindNodeAppended:
		return names.NodeAppended
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
