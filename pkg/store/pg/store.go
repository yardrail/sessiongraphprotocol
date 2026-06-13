package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/pg/sqlcdb"
)

// Edge is a directed graph edge returned by GetSessionGraph.
type Edge struct {
	FromID sgp.ID
	ToID   sgp.ID
	Kind   string // "parent" | "synthesized_from"
}

// EventRow pairs a sequence number with its deserialized event, used for
// gap-safe history replay in WatchSession.
type EventRow struct {
	Seq   int64
	Event sgp.Event
}

// SessionInfo bundles the return values of GetSession to stay within the
// function-result-limit of 3.
type SessionInfo struct {
	Session sgp.Session
	HeadID  sgp.ID
	Status  SessionStatus
}

// Store implements sgp.Store and exposes extended graph query methods backed
// by Postgres.
type Store struct {
	pool    *pgxpool.Pool
	queries *sqlcdb.Queries
	broker  *NotifyBroker
}

var _ sgp.Store = (*Store)(nil)

// NewStore creates a Store.
func NewStore(pool *pgxpool.Pool, broker *NotifyBroker) *Store {
	return &Store{pool: pool, queries: sqlcdb.New(pool), broker: broker}
}

// AppendEvent appends the event to the event log, mirrors it into sgp_nodes,
// then notifies live subscribers via pg_notify.
func (s *Store) AppendEvent(ctx context.Context, sessionID sgp.ID, event sgp.Event) error {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := sqlcdb.New(tx)

		err := q.AcquireSessionLock(ctx, string(sessionID))
		if err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}

		seq, err := q.InsertEvent(ctx, sqlcdb.InsertEventParams{
			SessionID: string(sessionID),
			EventJson: json.RawMessage(eventJSON),
		})
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		err = s.applyNodes(ctx, q, event)
		if err != nil {
			return fmt.Errorf("nodes write: %w", err)
		}

		err = q.NotifySession(ctx, sqlcdb.NotifySessionParams{
			Channel: "sgp:" + string(sessionID),
			Payload: strconv.FormatInt(seq, 10),
		})
		if err != nil {
			return fmt.Errorf("pg_notify: %w", err)
		}

		return nil
	})
}

// LoadEvents returns all events for sessionID ordered by seq.
// Returns sgp.ErrGraphNotFound if no events exist.
func (s *Store) LoadEvents(ctx context.Context, sessionID sgp.ID) ([]sgp.Event, error) {
	rows, err := s.queries.LoadEventsBySession(ctx, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}

	events := make([]sgp.Event, 0, len(rows))

	for _, data := range rows {
		var event sgp.Event

		err = json.Unmarshal(data, &event)
		if err != nil {
			return nil, fmt.Errorf("unmarshal event: %w", err)
		}

		event.Kind = sgp.ClassifyEvent(event)
		events = append(events, event)
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("%w: %s", sgp.ErrGraphNotFound, sessionID)
	}

	return events, nil
}

// LoadEventsWithSeq returns events paired with their sequence numbers, ordered
// by seq. Used by WatchSession for gap-safe history replay.
func (s *Store) LoadEventsWithSeq(ctx context.Context, sessionID sgp.ID) ([]EventRow, error) {
	rows, err := s.queries.LoadEventsWithSeq(ctx, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("query events with seq: %w", err)
	}

	result := make([]EventRow, 0, len(rows))

	for _, r := range rows {
		var event sgp.Event

		err = json.Unmarshal(r.EventJson, &event)
		if err != nil {
			return nil, fmt.Errorf("unmarshal event row: %w", err)
		}

		event.Kind = sgp.ClassifyEvent(event)
		result = append(result, EventRow{Seq: r.Seq, Event: event})
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("%w: %s", sgp.ErrGraphNotFound, sessionID)
	}

	return result, nil
}

// GetResumeContext returns the canonical lineage (root → nodeID) by traversing
// parent links in sgp_nodes, then hydrating nodes from the event log.
func (s *Store) GetResumeContext(ctx context.Context, nodeID sgp.ID) ([]sgp.Node, error) {
	ids, err := s.queries.GetLineage(ctx, string(nodeID))
	if err != nil {
		return nil, fmt.Errorf("lineage query: %w", err)
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: %s", sgp.ErrNodeNotFound, nodeID)
	}

	nodeMap, err := s.fetchNodesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]sgp.Node, 0, len(ids))

	for _, id := range ids {
		n, ok := nodeMap[id]
		if !ok {
			return nil, fmt.Errorf("%w: %s", sgp.ErrNodeNotFound, id)
		}

		result = append(result, n)
	}

	return result, nil
}

// GetSessionGraph returns all nodes and edges for a session.
func (s *Store) GetSessionGraph(
	ctx context.Context,
	sessionID sgp.ID,
) ([]sgp.Node, []Edge, error) {
	nodes, err := s.loadSessionNodes(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	edges, err := s.loadSessionEdges(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}

	return nodes, edges, nil
}

// ListSessions returns sessions ordered by first event time, with keyset pagination.
// pageToken is the last seen session_id.
func (s *Store) ListSessions(
	ctx context.Context,
	limit int,
	pageToken string,
) ([]sgp.Session, string, error) {
	if limit <= 0 {
		limit = 50
	}

	rawRows, err := s.fetchFirstSessionEvents(ctx, int32(limit+1), pageToken)
	if err != nil {
		return nil, "", err
	}

	sessions := make([]sgp.Session, 0, len(rawRows))

	for _, r := range rawRows {
		var event sgp.Event

		unmarshalErr := json.Unmarshal(r, &event)
		if unmarshalErr != nil {
			continue
		}

		sessions = append(sessions, sgp.Session{
			ID:          event.SessionID,
			Timestamp:   event.Timestamp,
			SpawnedFrom: event.SpawnedFrom,
		})
	}

	var nextToken string

	if len(sessions) > limit {
		sessions = sessions[:limit]
		nextToken = string(sessions[len(sessions)-1].ID)
	}

	return sessions, nextToken, nil
}

// GetSession returns session metadata and current HEAD node id.
func (s *Store) GetSession(
	ctx context.Context,
	sessionID sgp.ID,
) (SessionInfo, error) {
	events, err := s.LoadEvents(ctx, sessionID)
	if err != nil {
		return SessionInfo{}, err
	}

	graph, err := sgp.RestoreFromEvents(events)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("restore graph: %w", err)
	}

	sess := graph.Session()
	head, _ := graph.Head()

	status := SessionStatusOpen

	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == sgp.EventKindSessionEnded {
			status = SessionStatusClosed

			break
		}
	}

	return SessionInfo{Session: sess, HeadID: head.ID, Status: status}, nil
}

// GetNode fetches a single node by ID from the event log.
func (s *Store) GetNode(ctx context.Context, nodeID sgp.ID) (sgp.Node, error) {
	nodeMap, err := s.fetchNodesByIDs(ctx, []string{string(nodeID)})
	if err != nil {
		return sgp.Node{}, err
	}

	n, ok := nodeMap[string(nodeID)]
	if !ok {
		return sgp.Node{}, fmt.Errorf("%w: %s", sgp.ErrNodeNotFound, nodeID)
	}

	return n, nil
}

// Subscribe returns a channel that receives Observations for sessionID.
func (s *Store) Subscribe(ctx context.Context, sessionID sgp.ID) (<-chan Observation, func()) {
	return s.broker.Subscribe(ctx, string(sessionID))
}

func (s *Store) applyNodes(ctx context.Context, q *sqlcdb.Queries, event sgp.Event) error {
	if event.Kind != sgp.EventKindNodeAppended && event.Kind != sgp.EventKindHistoryRewritten {
		return nil
	}

	n := event.Node
	if n == nil {
		return nil
	}

	parentIDs := make([]string, len(n.ParentIDs))
	for i, id := range n.ParentIDs {
		parentIDs[i] = string(id)
	}

	synthFrom := make([]string, len(n.SynthesizedFrom))
	for i, id := range n.SynthesizedFrom {
		synthFrom[i] = string(id)
	}

	return q.InsertNode(ctx, sqlcdb.InsertNodeParams{
		ID:        string(n.ID),
		SessionID: string(n.SessionID),
		Role:      string(n.Message.Role()),
		ParentIds: parentIDs,
		SynthFrom: synthFrom,
	})
}

// fetchFirstSessionEvents retrieves the first event JSON per session for pagination.
func (s *Store) fetchFirstSessionEvents(
	ctx context.Context,
	lim int32,
	pageToken string,
) ([]json.RawMessage, error) {
	if pageToken == "" {
		rows, err := s.queries.ListSessionsFirst(ctx, lim)
		if err != nil {
			return nil, fmt.Errorf("list sessions query: %w", err)
		}

		result := make([]json.RawMessage, len(rows))
		for i, r := range rows {
			result[i] = r.EventJson
		}

		return result, nil
	}

	rows, err := s.queries.ListSessionsAfter(ctx, sqlcdb.ListSessionsAfterParams{
		PageToken: pageToken,
		Lim:       lim,
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions query: %w", err)
	}

	result := make([]json.RawMessage, len(rows))
	for i, r := range rows {
		result[i] = r.EventJson
	}

	return result, nil
}

func (s *Store) loadSessionNodes(ctx context.Context, sessionID sgp.ID) ([]sgp.Node, error) {
	rows, err := s.queries.GetNodesBySession(ctx, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("query session nodes: %w", err)
	}

	var nodes []sgp.Node

	for _, data := range rows {
		var event sgp.Event

		err = json.Unmarshal(data, &event)
		if err != nil {
			return nil, fmt.Errorf("unmarshal node event: %w", err)
		}

		if event.Node != nil {
			nodes = append(nodes, *event.Node)
		}
	}

	return nodes, nil
}

func (s *Store) loadSessionEdges(ctx context.Context, sessionID sgp.ID) ([]Edge, error) {
	rows, err := s.queries.GetEdgesBySession(ctx, string(sessionID))
	if err != nil {
		return nil, fmt.Errorf("edge query: %w", err)
	}

	edges := make([]Edge, 0, len(rows))

	for _, r := range rows {
		toID, ok := r.ToID.(string)
		if !ok {
			continue
		}

		edges = append(edges, Edge{
			FromID: sgp.ID(r.FromID),
			ToID:   sgp.ID(toID),
			Kind:   r.Kind,
		})
	}

	return edges, nil
}

// fetchNodesByIDs fetches nodes from the event log by ID.
func (s *Store) fetchNodesByIDs(ctx context.Context, ids []string) (map[string]sgp.Node, error) {
	if len(ids) == 0 {
		return make(map[string]sgp.Node), nil
	}

	rows, err := s.queries.FetchNodesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("fetch nodes by ids: %w", err)
	}

	result := make(map[string]sgp.Node, len(ids))

	for _, data := range rows {
		var event sgp.Event

		err = json.Unmarshal(data, &event)
		if err != nil {
			continue
		}

		if event.Node != nil {
			result[string(event.Node.ID)] = *event.Node
		}
	}

	return result, nil
}
