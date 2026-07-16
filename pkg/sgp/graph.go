package sgp

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrSessionClosed indicates that the graph has already emitted a terminal event.
	ErrSessionClosed = errors.New("session graph is closed")
	// ErrSessionNotStarted indicates that Start has not been called on the graph.
	ErrSessionNotStarted = errors.New("session graph has not been started")
	// ErrSessionAlreadyStarted indicates that Start was called on a graph that has already started.
	ErrSessionAlreadyStarted = errors.New("session graph has already been started")
	// ErrNodeNotFound indicates that the requested node does not exist.
	ErrNodeNotFound = errors.New("node not found")
	// ErrInvalidRoot indicates that a root append was attempted after initialization.
	ErrInvalidRoot = errors.New("root node must be the first node in the graph")

	errInvalidEndReason   = errors.New("invalid end reason")
	errMissingEventName   = errors.New("missing event name for kind")
	errMessageSubtype     = errors.New("message must have exactly one subtype set")
	errNodeReferenceEmpty = errors.New("node reference cannot be empty")
)

// IDGenerator creates stable string identifiers for sessions and nodes.
type IDGenerator func() ID

type config struct {
	idGenerator IDGenerator
	eventNames  EventNames
	sessionID   ID
	spawnedFrom *SpawnReference
}

// Option configures a Graph.
type Option func(*config)

// WithIDGenerator overrides the graph ID generator.
func WithIDGenerator(generator IDGenerator) Option {
	return func(cfg *config) {
		if generator == nil {
			return
		}

		cfg.idGenerator = generator
	}
}

// WithEventNames overrides the emitted event strings.
func WithEventNames(names EventNames) Option {
	return func(cfg *config) {
		cfg.eventNames = names
	}
}

// WithSessionID forces the graph to use a specific session ID.
func WithSessionID(sessionID ID) Option {
	return func(cfg *config) {
		cfg.sessionID = sessionID
	}
}

// WithSpawnedFrom marks the graph as a subagent session.
func WithSpawnedFrom(reference SpawnReference) Option {
	return func(cfg *config) {
		copied := reference
		cfg.spawnedFrom = &copied
	}
}

// Graph stores an in-memory SGP session.
type Graph struct {
	mu             sync.RWMutex
	session        Session
	eventNames     EventNames
	idGenerator    IDGenerator
	nodes          map[ID]Node
	children       map[ID][]ID
	events         []Event
	headID         ID
	terminalNodeID ID
	endReason      EndReason
	started        bool
	closed         bool
}

// NewGraph creates a new in-memory session graph. Call [Graph.Start] to formally
// begin the session and emit the session.start event. Append, AppendTypedNode,
// and End all require Start to have been called first.
func NewGraph(options ...Option) *Graph {
	cfg := config{
		idGenerator: func() ID {
			return ID(uuid.NewString())
		},
		eventNames: DefaultEventNames(),
	}

	for _, option := range options {
		option(&cfg)
	}

	if cfg.sessionID == "" {
		cfg.sessionID = cfg.idGenerator()
	}

	return &Graph{
		session: Session{
			ID:          cfg.sessionID,
			SpawnedFrom: copySpawnReference(cfg.spawnedFrom),
		},
		eventNames:  cfg.eventNames,
		idGenerator: cfg.idGenerator,
		nodes:       make(map[ID]Node),
		children:    make(map[ID][]ID),
	}
}

// Start formally begins the session and emits the session.start event. It must
// be called before Append, AppendTypedNode, or End. Returns [ErrSessionAlreadyStarted]
// if called more than once, and [ErrSessionClosed] if the graph is already closed.
func (graph *Graph) Start() (Event, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	if graph.closed {
		return Event{}, ErrSessionClosed
	}

	if graph.started {
		return Event{}, ErrSessionAlreadyStarted
	}

	graph.started = true
	graph.session.Timestamp = time.Now().UTC()

	event := Event{
		Kind:        EventKindSessionStart,
		Event:       graph.eventNames.Name(EventKindSessionStart),
		SessionID:   graph.session.ID,
		Timestamp:   graph.session.Timestamp,
		SpawnedFrom: copySpawnReference(graph.session.SpawnedFrom),
	}

	graph.events = append(graph.events, event)

	return copyEvent(event), nil
}

// Session returns the graph's session metadata.
func (graph *Graph) Session() Session {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	return Session{
		ID:          graph.session.ID,
		Timestamp:   graph.session.Timestamp,
		SpawnedFrom: copySpawnReference(graph.session.SpawnedFrom),
	}
}

// Events returns a copy of the emitted event stream.
func (graph *Graph) Events() []Event {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	return copyEvents(graph.events)
}

// Head returns the current canonical head node.
func (graph *Graph) Head() (Node, bool) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	if graph.headID == "" {
		return Node{}, false
	}

	node, ok := graph.nodes[graph.headID]
	if !ok {
		return Node{}, false
	}

	return copyNode(node), true
}

// Node returns a copy of the requested node.
func (graph *Graph) Node(nodeID ID) (Node, error) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	node, ok := graph.nodes[nodeID]
	if !ok {
		return Node{}, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
	}

	return copyNode(node), nil
}

// Append creates a new node and emits a node appended event.
func (graph *Graph) Append(message Message, parentIDs ...ID) (Node, Event, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	node, event, err := graph.appendNode(EventKindNodeAppended, message, parentIDs)
	if err != nil {
		return Node{}, Event{}, err
	}

	return copyNode(node), copyEvent(event), nil
}

// AppendTypedNode creates a typed node with the given kind, message, extra edges, and content.
// The content parameter must be one of *MemoryContent, *SkillContent, *IdentityContent,
// *SleepContent, or nil for experience nodes. parentIDs follow the same rules as Append.
func (graph *Graph) AppendTypedNode(
	kind NodeKind,
	msg Message,
	edges []EdgeRef,
	content interface{},
	parentIDs ...ID,
) (Node, ID, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	node, _, err := graph.appendNode(EventKindNodeAppended, msg, parentIDs)
	if err != nil {
		return Node{}, "", err
	}

	node.Kind = kind
	node.Edges = append(node.Edges, edges...)

	switch c := content.(type) {
	case *MemoryContent:
		node.Memory = c
	case *SkillContent:
		node.Skill = c
	case *IdentityContent:
		node.Identity = c
	case *SleepContent:
		node.Sleep = c
	}

	graph.nodes[node.ID] = copyNode(node)
	n := copyNode(node)
	graph.events[len(graph.events)-1].Node = &n

	return copyNode(node), node.ID, nil
}

// End emits a session ended event. reason must be one of [EndReasonComplete] or
// [EndReasonFailed]. Returns [ErrSessionNotStarted] if Start has not been called,
// and [ErrSessionClosed] if End has already been called. terminal_node_id in the
// emitted event is empty when End is called on a started graph with no nodes.
func (graph *Graph) End(reason EndReason) (Event, error) {
	if reason != EndReasonComplete && reason != EndReasonFailed {
		return Event{}, fmt.Errorf(
			"%w %q: must be %q or %q",
			errInvalidEndReason,
			reason,
			EndReasonComplete,
			EndReasonFailed,
		)
	}

	graph.mu.Lock()
	defer graph.mu.Unlock()

	if graph.closed {
		return Event{}, ErrSessionClosed
	}

	if !graph.started {
		return Event{}, ErrSessionNotStarted
	}

	graph.closed = true
	graph.terminalNodeID = graph.headID
	graph.endReason = reason

	event := Event{
		Kind:           EventKindSessionEnded,
		Event:          graph.eventNames.Name(EventKindSessionEnded),
		SessionID:      graph.session.ID,
		Timestamp:      time.Now().UTC(),
		TerminalNodeID: graph.terminalNodeID,
		Reason:         reason,
	}

	graph.events = append(graph.events, event)

	return copyEvent(event), nil
}

// ResumeNodes returns the canonical lineage from the root to the requested node.
func (graph *Graph) ResumeNodes(nodeID ID) ([]Node, error) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	lineage, err := graph.resumeNodes(nodeID)
	if err != nil {
		return nil, err
	}

	return lineage, nil
}

// ResumeMessages returns the canonical message history from the root to the requested node.
func (graph *Graph) ResumeMessages(nodeID ID) ([]Message, error) {
	lineage, err := graph.ResumeNodes(nodeID)
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(lineage))
	for _, node := range lineage {
		messages = append(messages, node.Message)
	}

	return messages, nil
}

// NeedsResponse reports whether a leaf node implies pending inference work.
// AdvanceHead walks single-child EdgeKindParent edges from fromID until reaching
// a node with no EdgeKindParent children, returning the leaf ID. If fromID already
// has no children, it returns fromID. If fromID has multiple EdgeKindParent children
// (should not occur with proper locking), it stops and returns the current node.
// AdvanceHead is safe for concurrent use.
func (graph *Graph) AdvanceHead(fromID ID) (ID, error) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	if _, ok := graph.nodes[fromID]; !ok {
		return "", fmt.Errorf("%w: %s", ErrNodeNotFound, fromID)
	}

	current := fromID
	for {
		children := graph.children[current]
		if len(children) == 0 {
			return current, nil
		}
		if len(children) > 1 {
			// Real branch point — don't auto-advance.
			return current, nil
		}
		current = children[0]
	}
}

func (graph *Graph) NeedsResponse(nodeID ID) (bool, error) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	node, ok := graph.nodes[nodeID]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
	}

	return len(graph.children[nodeID]) == 0 && requiresResponse(node.Message), nil
}

func (graph *Graph) appendNode(
	kind EventKind,
	message Message,
	parentIDs []ID,
) (Node, Event, error) {
	if graph.closed {
		return Node{}, Event{}, ErrSessionClosed
	}

	if !graph.started {
		return Node{}, Event{}, ErrSessionNotStarted
	}

	if graph.eventNames.Name(kind) == "" {
		return Node{}, Event{}, fmt.Errorf("%w %d", errMissingEventName, kind)
	}

	if !message.valid() {
		return Node{}, Event{}, errMessageSubtype
	}

	if len(parentIDs) == 0 && len(graph.nodes) != 0 {
		return Node{}, Event{}, ErrInvalidRoot
	}

	validatedParents, err := graph.validateNodeReferences(parentIDs)
	if err != nil {
		return Node{}, Event{}, err
	}

	edges := make([]EdgeRef, 0, len(validatedParents))
	for _, pid := range validatedParents {
		edges = append(edges, EdgeRef{Kind: EdgeKindParent, NodeID: pid})
	}

	node := Node{
		ID:        graph.idGenerator(),
		SessionID: graph.session.ID,
		Timestamp: time.Now().UTC(),
		Edges:     edges,
		Message:   copyMessage(message),
	}

	graph.nodes[node.ID] = copyNode(node)
	for _, parentID := range node.Parents() {
		graph.children[parentID] = append(graph.children[parentID], node.ID)
	}

	graph.headID = node.ID

	event := Event{
		Kind:      kind,
		Event:     graph.eventNames.Name(kind),
		SessionID: graph.session.ID,
		Timestamp: node.Timestamp,
		Node:      copyNodePointer(&node),
	}

	graph.events = append(graph.events, event)

	return node, event, nil
}

func (graph *Graph) validateNodeReferences(ids []ID) ([]ID, error) {
	validated := make([]ID, 0, len(ids))
	seen := make(map[ID]struct{}, len(ids))

	for _, nodeID := range ids {
		if nodeID == "" {
			return nil, errNodeReferenceEmpty
		}

		if _, exists := graph.nodes[nodeID]; !exists {
			return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
		}

		if _, duplicate := seen[nodeID]; duplicate {
			continue
		}

		seen[nodeID] = struct{}{}
		validated = append(validated, nodeID)
	}

	return validated, nil
}

func (graph *Graph) resumeNodes(nodeID ID) ([]Node, error) {
	node, ok := graph.nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
	}

	lineage := []Node{copyNode(node)}
	current := node

	for len(current.Parents()) != 0 {
		parentID := current.Parents()[0]

		parent, exists := graph.nodes[parentID]
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, parentID)
		}

		lineage = append(lineage, copyNode(parent))
		current = parent
	}

	slices.Reverse(lineage)

	return lineage, nil
}

func requiresResponse(msg Message) bool {
	return msg.User != nil || msg.Tool != nil
}

func copyNode(node Node) Node {
	return Node{
		ID:              node.ID,
		SessionID:       node.SessionID,
		Timestamp:       node.Timestamp,
		Message:         node.Message,
		Kind:            node.Kind,
		Edges:           append([]EdgeRef(nil), node.Edges...),
		Archived:        node.Archived,
		Memory:          copyMemoryContent(node.Memory),
		Skill:           copySkillContent(node.Skill),
		Identity:        copyIdentityContent(node.Identity),
		Sleep:           copySleepContent(node.Sleep),
	}
}

func copyMemoryContent(m *MemoryContent) *MemoryContent {
	if m == nil {
		return nil
	}
	cp := *m
	if len(m.Tags) > 0 {
		cp.Tags = append([]string(nil), m.Tags...)
	}
	return &cp
}

func copySkillContent(s *SkillContent) *SkillContent {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func copyIdentityContent(i *IdentityContent) *IdentityContent {
	if i == nil {
		return nil
	}
	return &IdentityContent{
		Traits: append([]string(nil), i.Traits...),
		Values: append([]string(nil), i.Values...),
		Goals:  append([]string(nil), i.Goals...),
	}
}

func copySleepContent(s *SleepContent) *SleepContent {
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func copyMessage(msg Message) Message {
	return Message{
		System:    copySystemMessage(msg.System),
		User:      copyUserMessage(msg.User),
		Assistant: copyAssistantMessage(msg.Assistant),
		Tool:      copyToolMessage(msg.Tool),
	}
}

func copySystemMessage(m *SystemMessage) *SystemMessage {
	if m == nil {
		return nil
	}

	cp := *m

	return &cp
}

func copyUserMessage(m *UserMessage) *UserMessage {
	if m == nil {
		return nil
	}

	return &UserMessage{Parts: copyContentParts(m.Parts)}
}

func copyAssistantMessage(m *AssistantMessage) *AssistantMessage {
	if m == nil {
		return nil
	}

	cp := &AssistantMessage{Refusal: m.Refusal}
	cp.Parts = copyContentParts(m.Parts)

	if len(m.ToolCalls) > 0 {
		cp.ToolCalls = make([]ToolCall, len(m.ToolCalls))
		copy(cp.ToolCalls, m.ToolCalls)
	}

	return cp
}

func copyToolMessage(m *ToolMessage) *ToolMessage {
	if m == nil {
		return nil
	}

	return &ToolMessage{
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
		Parts:      copyContentParts(m.Parts),
		IsError:    m.IsError,
	}
}

func copyContentParts(parts []ContentPart) []ContentPart {
	if len(parts) == 0 {
		return nil
	}

	cp := make([]ContentPart, len(parts))

	for i, part := range parts {
		cp[i] = copyContentPart(part)
	}

	return cp
}

func copyContentPart(part ContentPart) ContentPart {
	var cp ContentPart

	if part.Text != nil {
		v := *part.Text
		cp.Text = &v
	}

	if part.Image != nil {
		cp.Image = &ImagePart{BlobPart: copyBlobPart(part.Image.BlobPart)}
	}

	if part.Audio != nil {
		cp.Audio = &AudioPart{BlobPart: copyBlobPart(part.Audio.BlobPart)}
	}

	if part.Video != nil {
		cp.Video = &VideoPart{BlobPart: copyBlobPart(part.Video.BlobPart)}
	}

	if part.File != nil {
		cp.File = &FilePart{BlobPart: copyBlobPart(part.File.BlobPart), Name: part.File.Name}
	}

	return cp
}

func copyBlobPart(b BlobPart) BlobPart {
	if len(b.Data) == 0 {
		return BlobPart{MimeType: b.MimeType}
	}

	data := make([]byte, len(b.Data))
	copy(data, b.Data)

	return BlobPart{MimeType: b.MimeType, Data: data}
}

func copySpawnReference(reference *SpawnReference) *SpawnReference {
	if reference == nil {
		return nil
	}

	cloned := *reference

	return &cloned
}

func copyEvent(event Event) Event {
	return Event{
		Kind:           event.Kind,
		Event:          event.Event,
		SessionID:      event.SessionID,
		Timestamp:      event.Timestamp,
		SpawnedFrom:    copySpawnReference(event.SpawnedFrom),
		Node:           copyNodePointer(event.Node),
		TerminalNodeID: event.TerminalNodeID,
		Reason:         event.Reason,
	}
}

func copyEvents(events []Event) []Event {
	cloned := make([]Event, 0, len(events))
	for _, event := range events {
		cloned = append(cloned, copyEvent(event))
	}

	return cloned
}

func copyNodePointer(node *Node) *Node {
	if node == nil {
		return nil
	}

	cloned := copyNode(*node)

	return &cloned
}
