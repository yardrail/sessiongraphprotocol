package sessiongraphprotocol

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
	// ErrNodeNotFound indicates that the requested node does not exist.
	ErrNodeNotFound = errors.New("node not found")
	// ErrInvalidRoot indicates that a root append was attempted after initialization.
	ErrInvalidRoot = errors.New("root node must be the first node in the graph")
)

// IDGenerator creates stable string identifiers for sessions and nodes.
type IDGenerator func() ID

// Clock reports the current time.
type Clock func() time.Time

type config struct {
	clock       Clock
	idGenerator IDGenerator
	eventNames  EventNames
	sessionID   ID
	spawnedFrom *SpawnReference
}

// Option configures a Graph.
type Option func(*config)

// WithClock overrides the graph clock.
func WithClock(clock Clock) Option {
	return func(cfg *config) {
		if clock == nil {
			return
		}

		cfg.clock = clock
	}
}

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
	clock          Clock
	idGenerator    IDGenerator
	nodes          map[ID]Node
	children       map[ID][]ID
	events         []Event
	headID         ID
	terminalNodeID ID
	closed         bool
}

// NewGraph creates a new in-memory session graph and emits a session start event.
func NewGraph(options ...Option) *Graph {
	cfg := config{
		clock: func() time.Time {
			return time.Now().UTC()
		},
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

	graph := &Graph{
		session: Session{
			ID:          cfg.sessionID,
			Timestamp:   cfg.clock(),
			SpawnedFrom: copySpawnReference(cfg.spawnedFrom),
		},
		eventNames:  cfg.eventNames,
		clock:       cfg.clock,
		idGenerator: cfg.idGenerator,
		nodes:       make(map[ID]Node),
		children:    make(map[ID][]ID),
	}

	graph.events = append(graph.events, Event{
		Kind:        EventKindSessionStart,
		Event:       graph.eventNames.Name(EventKindSessionStart),
		SessionID:   graph.session.ID,
		Timestamp:   graph.session.Timestamp,
		SpawnedFrom: copySpawnReference(graph.session.SpawnedFrom),
	})

	return graph
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
func (graph *Graph) Append(message Message, metadata map[string]any, parentIDs ...ID) (Node, Event, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	node, event, err := graph.appendNode(EventKindNodeAppended, message, metadata, parentIDs, nil)
	if err != nil {
		return Node{}, Event{}, err
	}

	return copyNode(node), copyEvent(event), nil
}

// Rewrite creates a rewrite node and emits a history rewritten event.
func (graph *Graph) Rewrite(message Message, metadata map[string]any, parentID ID, synthesizedFrom ...ID) (Node, Event, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	if parentID == "" {
		return Node{}, Event{}, errors.New("rewrite requires a canonical parent")
	}

	if len(synthesizedFrom) == 0 {
		return Node{}, Event{}, errors.New("rewrite requires at least one synthesized source")
	}

	node, event, err := graph.appendNode(
		EventKindHistoryRewritten,
		message,
		metadata,
		[]ID{parentID},
		synthesizedFrom,
	)
	if err != nil {
		return Node{}, Event{}, err
	}

	return copyNode(node), copyEvent(event), nil
}

// End emits a session ended event using the current head as the terminal node.
func (graph *Graph) End() (Event, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()

	if graph.closed {
		return Event{}, ErrSessionClosed
	}

	if graph.headID == "" {
		return Event{}, errors.New("cannot end a session without nodes")
	}

	graph.closed = true
	graph.terminalNodeID = graph.headID

	event := Event{
		Kind:           EventKindSessionEnded,
		Event:          graph.eventNames.Name(EventKindSessionEnded),
		SessionID:      graph.session.ID,
		Timestamp:      graph.clock(),
		TerminalNodeID: graph.terminalNodeID,
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
func (graph *Graph) NeedsResponse(nodeID ID) (bool, error) {
	graph.mu.RLock()
	defer graph.mu.RUnlock()

	node, ok := graph.nodes[nodeID]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
	}

	return len(graph.children[nodeID]) == 0 && requiresResponse(node.Message.Role), nil
}

func (graph *Graph) appendNode(
	kind EventKind,
	message Message,
	metadata map[string]any,
	parentIDs []ID,
	synthesizedFrom []ID,
) (Node, Event, error) {
	if graph.closed {
		return Node{}, Event{}, ErrSessionClosed
	}

	if graph.eventNames.Name(kind) == "" {
		return Node{}, Event{}, fmt.Errorf("missing event name for kind %d", kind)
	}

	if message.Role == "" {
		return Node{}, Event{}, errors.New("message role is required")
	}

	if len(parentIDs) == 0 && len(graph.nodes) != 0 {
		return Node{}, Event{}, ErrInvalidRoot
	}

	validatedParents, err := graph.validateNodeReferences(parentIDs)
	if err != nil {
		return Node{}, Event{}, err
	}

	validatedSources, err := graph.validateNodeReferences(synthesizedFrom)
	if err != nil {
		return Node{}, Event{}, err
	}

	node := Node{
		ID:              graph.idGenerator(),
		SessionID:       graph.session.ID,
		Timestamp:       graph.clock(),
		ParentIDs:       validatedParents,
		SynthesizedFrom: validatedSources,
		Message:         message,
		Metadata:        copyMetadata(metadata),
	}

	graph.nodes[node.ID] = copyNode(node)
	for _, parentID := range node.ParentIDs {
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
			return nil, errors.New("node reference cannot be empty")
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

	for len(current.ParentIDs) != 0 {
		parentID := current.ParentIDs[0]
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

func requiresResponse(role MessageRole) bool {
	return role == MessageRoleUser || role == MessageRoleTool
}

func copyNode(node Node) Node {
	return Node{
		ID:              node.ID,
		SessionID:       node.SessionID,
		Timestamp:       node.Timestamp,
		ParentIDs:       append([]ID(nil), node.ParentIDs...),
		SynthesizedFrom: append([]ID(nil), node.SynthesizedFrom...),
		Message:         node.Message,
		Metadata:        copyMetadata(node.Metadata),
	}
}

func copyMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}

	cloned := make(map[string]any, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}

	return cloned
}

func copySpawnReference(reference *SpawnReference) *SpawnReference {
	if reference == nil {
		return nil
	}

	copy := *reference

	return &copy
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
	}
}

func copyEvents(events []Event) []Event {
	cloned := make([]Event, 0, len(events))
	for _, event := range events {
		cloned = append(cloned, copyEvent(event))
	}

	return cloned
}

func copyNodeValue(node *Node) Node {
	if node == nil {
		return Node{}
	}

	return copyNode(*node)
}

func copyNodePointer(node *Node) *Node {
	if node == nil {
		return nil
	}

	copy := copyNode(*node)

	return &copy
}
