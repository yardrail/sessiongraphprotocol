package sgpsession

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	sgp "github.com/restrukt-ai/sessiongraphprotocol"
	"google.golang.org/adk/session"
)

type sessionKey struct {
	appName   string
	userID    string
	sessionID string
}

type trackedSession struct {
	appName   string
	userID    string
	sessionID string
	state     map[string]any
	events    []*session.Event
	updatedAt time.Time
	graph     *sgp.Graph
}

// Service implements ADK session.Service while persisting canonical history to SGP JSON snapshots.
type Service struct {
	mu        sync.RWMutex
	store     *sgp.JSONFileStore
	sessions  map[sessionKey]*trackedSession
	appState  map[string]map[string]any
	userState map[string]map[string]map[string]any
}

var _ session.Service = (*Service)(nil)

// NewService creates a new SGP-backed ADK session service.
func NewService(baseDir string) (*Service, error) {
	store, err := sgp.NewJSONFileStore(baseDir)
	if err != nil {
		return nil, err
	}

	return &Service{
		store:     store,
		sessions:  make(map[sessionKey]*trackedSession),
		appState:  make(map[string]map[string]any),
		userState: make(map[string]map[string]map[string]any),
	}, nil
}

func (service *Service) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("create request is nil")
	}
	if strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" {
		return nil, fmt.Errorf("app_name and user_id are required")
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}

	key := sessionKey{appName: req.AppName, userID: req.UserID, sessionID: sessionID}

	service.mu.Lock()
	defer service.mu.Unlock()

	if _, exists := service.sessions[key]; exists {
		return nil, fmt.Errorf("session %s already exists", sessionID)
	}

	appDelta, userDelta, sessDelta := extractStateDeltas(req.State)
	mergedApp := service.mergeAppStateLocked(req.AppName, appDelta)
	mergedUser := service.mergeUserStateLocked(req.AppName, req.UserID, userDelta)
	mergedSession := mergeMaps(mergedApp, mergedUser, sessDelta)

	graph := sgp.NewGraph(
		sgp.WithSessionID(sgp.ID(sessionID)),
	)
	if err := service.store.Save(ctx, graph); err != nil {
		return nil, fmt.Errorf("persist session graph: %w", err)
	}

	now := time.Now().UTC()
	tracked := &trackedSession{
		appName:   req.AppName,
		userID:    req.UserID,
		sessionID: sessionID,
		state:     mergedSession,
		events:    nil,
		updatedAt: now,
		graph:     graph,
	}
	service.sessions[key] = tracked

	return &session.CreateResponse{Session: copySession(tracked)}, nil
}

func (service *Service) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("get request is nil")
	}
	if strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return nil, fmt.Errorf("app_name, user_id, session_id are required")
	}

	key := sessionKey{appName: req.AppName, userID: req.UserID, sessionID: req.SessionID}

	service.mu.Lock()
	defer service.mu.Unlock()

	tracked, ok := service.sessions[key]
	if !ok {
		return nil, fmt.Errorf("session %s not found", req.SessionID)
	}

	state := mergeMaps(
		service.appState[req.AppName],
		service.userState[req.AppName][req.UserID],
		tracked.state,
	)

	events := tracked.events
	if !req.After.IsZero() || req.NumRecentEvents > 0 {
		events = filterEvents(events, req.After, req.NumRecentEvents)
	}

	copied := &trackedSession{
		appName:   tracked.appName,
		userID:    tracked.userID,
		sessionID: tracked.sessionID,
		state:     state,
		events:    cloneEvents(events),
		updatedAt: tracked.updatedAt,
	}

	return &session.GetResponse{Session: copySession(copied)}, nil
}

func (service *Service) List(_ context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("list request is nil")
	}
	if strings.TrimSpace(req.AppName) == "" {
		return nil, fmt.Errorf("app_name is required")
	}

	service.mu.RLock()
	defer service.mu.RUnlock()

	sessions := make([]session.Session, 0, len(service.sessions))
	for key, tracked := range service.sessions {
		if key.appName != req.AppName {
			continue
		}
		if req.UserID != "" && key.userID != req.UserID {
			continue
		}

		state := mergeMaps(service.appState[key.appName], service.userState[key.appName][key.userID], tracked.state)
		copied := &trackedSession{
			appName:   tracked.appName,
			userID:    tracked.userID,
			sessionID: tracked.sessionID,
			state:     state,
			events:    nil,
			updatedAt: tracked.updatedAt,
		}
		sessions = append(sessions, copySession(copied))
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })

	return &session.ListResponse{Sessions: sessions}, nil
}

func (service *Service) Delete(_ context.Context, req *session.DeleteRequest) error {
	if req == nil {
		return fmt.Errorf("delete request is nil")
	}
	if strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" || strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("app_name, user_id, session_id are required")
	}

	key := sessionKey{appName: req.AppName, userID: req.UserID, sessionID: req.SessionID}

	service.mu.Lock()
	defer service.mu.Unlock()

	if _, ok := service.sessions[key]; !ok {
		return nil
	}
	delete(service.sessions, key)

	return nil
}

func (service *Service) AppendEvent(ctx context.Context, cur session.Session, event *session.Event) error {
	if cur == nil {
		return fmt.Errorf("session is nil")
	}
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}

	key := sessionKey{appName: cur.AppName(), userID: cur.UserID(), sessionID: cur.ID()}

	service.mu.Lock()
	defer service.mu.Unlock()

	tracked, ok := service.sessions[key]
	if !ok {
		return fmt.Errorf("session %s not found", cur.ID())
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}

	appDelta, userDelta, sessDelta := extractStateDeltas(event.Actions.StateDelta)
	service.mergeAppStateLocked(key.appName, appDelta)
	service.mergeUserStateLocked(key.appName, key.userID, userDelta)
	maps.Copy(tracked.state, sessDelta)

	role := toSGPRole(event.Author)
	content := any("")
	if event.LLMResponse.Content != nil {
		content = event.LLMResponse.Content
	}

	metadata := map[string]any{
		"adk_event_id":      event.ID,
		"adk_author":        event.Author,
		"adk_branch":        event.Branch,
		"adk_invocation":    event.InvocationID,
		"adk_turn_complete": event.TurnComplete,
	}

	if err := appendSGPNodeLocked(tracked.graph, role, content, metadata); err != nil {
		return err
	}

	if err := service.store.Save(ctx, tracked.graph); err != nil {
		return fmt.Errorf("persist sgp graph: %w", err)
	}

	storedEvent := cloneEvent(event)
	storedEvent.Actions.StateDelta = trimTempState(storedEvent.Actions.StateDelta)
	tracked.events = append(tracked.events, storedEvent)
	tracked.updatedAt = storedEvent.Timestamp

	return nil
}

// EnsureSession guarantees a tracked session exists for OAC stream events.
func (service *Service) EnsureSession(ctx context.Context, appName, userID, sessionID string) error {
	if strings.TrimSpace(appName) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("app_name, user_id, session_id are required")
	}

	key := sessionKey{appName: appName, userID: userID, sessionID: sessionID}

	service.mu.RLock()
	_, ok := service.sessions[key]
	service.mu.RUnlock()
	if ok {
		return nil
	}

	_, err := service.Create(ctx, &session.CreateRequest{
		AppName:   appName,
		UserID:    userID,
		SessionID: sessionID,
	})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}

	return err
}

// IngestOrchestratorEvent appends an orchestrator-delivered event to the SGP graph.
func (service *Service) IngestOrchestratorEvent(
	ctx context.Context,
	appName, userID, sessionID, channel, contentType string,
	payload []byte,
) error {
	if err := service.EnsureSession(ctx, appName, userID, sessionID); err != nil {
		return err
	}

	key := sessionKey{appName: appName, userID: userID, sessionID: sessionID}

	service.mu.Lock()
	defer service.mu.Unlock()

	tracked, ok := service.sessions[key]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	metadata := map[string]any{
		"oac_channel":       channel,
		"oac_content_type":  contentType,
		"oac_payload_bytes": len(payload),
	}

	if err := appendSGPNodeLocked(tracked.graph, sgp.MessageRoleTool, string(payload), metadata); err != nil {
		return err
	}

	if err := service.store.Save(ctx, tracked.graph); err != nil {
		return fmt.Errorf("persist sgp graph: %w", err)
	}

	tracked.updatedAt = time.Now().UTC()

	return nil
}

func appendSGPNodeLocked(graph *sgp.Graph, role sgp.MessageRole, content any, metadata map[string]any) error {
	head, hasHead := graph.Head()
	if hasHead {
		_, _, err := graph.Append(sgp.Message{Role: role, Content: content}, metadata, head.ID)
		if err != nil {
			return fmt.Errorf("append sgp node: %w", err)
		}
		return nil
	}

	_, _, err := graph.Append(sgp.Message{Role: role, Content: content}, metadata)
	if err != nil {
		return fmt.Errorf("append sgp root node: %w", err)
	}

	return nil
}

func (service *Service) mergeAppStateLocked(appName string, delta map[string]any) map[string]any {
	if service.appState[appName] == nil {
		service.appState[appName] = make(map[string]any)
	}
	maps.Copy(service.appState[appName], delta)
	return maps.Clone(service.appState[appName])
}

func (service *Service) mergeUserStateLocked(appName, userID string, delta map[string]any) map[string]any {
	if service.userState[appName] == nil {
		service.userState[appName] = make(map[string]map[string]any)
	}
	if service.userState[appName][userID] == nil {
		service.userState[appName][userID] = make(map[string]any)
	}
	maps.Copy(service.userState[appName][userID], delta)
	return maps.Clone(service.userState[appName][userID])
}

func extractStateDeltas(delta map[string]any) (map[string]any, map[string]any, map[string]any) {
	appDelta := make(map[string]any)
	userDelta := make(map[string]any)
	sessionDelta := make(map[string]any)
	for key, value := range delta {
		switch {
		case strings.HasPrefix(key, "app:"):
			appDelta[key] = value
		case strings.HasPrefix(key, "user:"):
			userDelta[key] = value
		default:
			sessionDelta[key] = value
		}
	}
	return appDelta, userDelta, sessionDelta
}

func trimTempState(delta map[string]any) map[string]any {
	if len(delta) == 0 {
		return nil
	}
	result := make(map[string]any, len(delta))
	for key, value := range delta {
		if strings.HasPrefix(key, "temp:") {
			continue
		}
		result[key] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func filterEvents(events []*session.Event, after time.Time, numRecent int) []*session.Event {
	filtered := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if !after.IsZero() && event.Timestamp.Before(after) {
			continue
		}
		filtered = append(filtered, cloneEvent(event))
	}
	if numRecent > 0 && len(filtered) > numRecent {
		filtered = filtered[len(filtered)-numRecent:]
	}
	return filtered
}

func mergeMaps(parts ...map[string]any) map[string]any {
	merged := make(map[string]any)
	for _, part := range parts {
		maps.Copy(merged, part)
	}
	return merged
}

func cloneEvent(event *session.Event) *session.Event {
	if event == nil {
		return nil
	}
	copy := *event
	copy.Actions.StateDelta = maps.Clone(event.Actions.StateDelta)
	copy.Actions.ArtifactDelta = maps.Clone(event.Actions.ArtifactDelta)
	copy.LongRunningToolIDs = append([]string(nil), event.LongRunningToolIDs...)
	return &copy
}

func cloneEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		out = append(out, cloneEvent(event))
	}
	return out
}

type stateView struct {
	mu    *sync.RWMutex
	state map[string]any
}

var _ session.State = (*stateView)(nil)

func (state *stateView) Get(key string) (any, error) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	value, ok := state.state[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (state *stateView) Set(key string, value any) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.state[key] = value
	return nil
}

func (state *stateView) All() iter.Seq2[string, any] {
	state.mu.RLock()
	copy := maps.Clone(state.state)
	state.mu.RUnlock()
	return func(yield func(string, any) bool) {
		for key, value := range copy {
			if !yield(key, value) {
				return
			}
		}
	}
}

type eventsView []*session.Event

var _ session.Events = eventsView(nil)

func (events eventsView) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range events {
			if !yield(event) {
				return
			}
		}
	}
}

func (events eventsView) Len() int {
	return len(events)
}

func (events eventsView) At(index int) *session.Event {
	if index < 0 || index >= len(events) {
		return nil
	}
	return events[index]
}

type sessionView struct {
	appName   string
	userID    string
	sessionID string
	state     *stateView
	events    eventsView
	updatedAt time.Time
}

var _ session.Session = (*sessionView)(nil)

func (sessionObj *sessionView) ID() string {
	return sessionObj.sessionID
}

func (sessionObj *sessionView) AppName() string {
	return sessionObj.appName
}

func (sessionObj *sessionView) UserID() string {
	return sessionObj.userID
}

func (sessionObj *sessionView) State() session.State {
	return sessionObj.state
}

func (sessionObj *sessionView) Events() session.Events {
	return sessionObj.events
}

func (sessionObj *sessionView) LastUpdateTime() time.Time {
	return sessionObj.updatedAt
}

func copySession(tracked *trackedSession) session.Session {
	state := maps.Clone(tracked.state)
	events := cloneEvents(tracked.events)
	mu := &sync.RWMutex{}
	return &sessionView{
		appName:   tracked.appName,
		userID:    tracked.userID,
		sessionID: tracked.sessionID,
		state:     &stateView{mu: mu, state: state},
		events:    eventsView(events),
		updatedAt: tracked.updatedAt,
	}
}

func toSGPRole(author string) sgp.MessageRole {
	if strings.EqualFold(author, "user") {
		return sgp.MessageRoleUser
	}
	if strings.EqualFold(author, "tool") {
		return sgp.MessageRoleTool
	}
	return sgp.MessageRoleAssistant
}
