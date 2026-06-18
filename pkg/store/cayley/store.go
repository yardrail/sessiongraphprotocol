// Package cayleystore implements sgp.Store backed by a Cayley quad store.
package cayleystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/cayleygraph/cayley/graph"
	"github.com/cayleygraph/cayley/graph/path"
	"github.com/cayleygraph/quad"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

// Edge is a directed graph edge returned by GetSessionGraph.
type Edge struct {
	FromID sgp.ID
	ToID   sgp.ID
	Kind   string // "parent" | "synthesized_from"
}

// Store implements sgp.Store and sgp.Watcher backed by a Cayley quad store.
type Store struct {
	qs graph.QuadStore
	watcherMixin
}

var _ sgp.Store = (*Store)(nil)
var _ sgp.Watcher = (*Store)(nil)

// New creates a Store backed by qs.
func New(qs graph.QuadStore) *Store {
	s := &Store{qs: qs}
	s.watcherMixin.init()
	return s
}

// CreateSession persists session metadata and registers in the global sessions index.
func (s *Store) CreateSession(ctx context.Context, sess sgp.Session) error {
	deltas := sessionToDeltas(sess)
	deltas = append(deltas, addDelta(
		quad.IRI(globalSessions), predMember, quad.IRI(string(sess.ID)), quad.IRI(globalLabel),
	))
	return s.qs.ApplyDeltas(deltas, graph.IgnoreOpts{IgnoreDup: true})
}

// WriteNode persists node quads and upserts the session's head pointer.
func (s *Store) WriteNode(ctx context.Context, node sgp.Node) error {
	// Verify session exists.
	_, _, err := s.GetSession(ctx, node.SessionID)
	if err != nil {
		if errors.Is(err, sgp.ErrSessionNotFound) {
			return fmt.Errorf("%w: %s", sgp.ErrSessionNotFound, node.SessionID)
		}
		return fmt.Errorf("check session: %w", err)
	}

	deltas := nodeToDeltas(node)

	// Upsert head: delete old head quad if any, add new.
	sessIRI := quad.IRI(string(node.SessionID))
	sessLabel := quad.IRI(string(node.SessionID))
	oldHeads, _ := s.outValues(ctx, string(node.SessionID), predHead)
	for _, v := range oldHeads {
		deltas = append(deltas, delDelta(sessIRI, predHead, v, sessLabel))
	}
	deltas = append(deltas, addDelta(sessIRI, predHead, quad.IRI(string(node.ID)), sessLabel))

	if err := s.qs.ApplyDeltas(deltas, graph.IgnoreOpts{IgnoreDup: true}); err != nil {
		return err
	}

	s.publish(node.SessionID, node)
	return nil
}

// EndSession marks a session closed with the given reason and terminal node.
func (s *Store) EndSession(ctx context.Context, sessionID sgp.ID, reason sgp.EndReason, terminalNodeID sgp.ID) error {
	sessIRI := quad.IRI(string(sessionID))
	sessLabel := quad.IRI(string(sessionID))

	// Upsert status: delete old, add "closed".
	oldStatus, _ := s.outValues(ctx, string(sessionID), predStatus)
	deltas := make([]graph.Delta, 0, len(oldStatus)+3)
	for _, v := range oldStatus {
		deltas = append(deltas, delDelta(sessIRI, predStatus, v, sessLabel))
	}

	deltas = append(deltas,
		addDelta(sessIRI, predStatus, quad.String(statusClosed), sessLabel),
		addDelta(sessIRI, predEndReason, quad.String(string(reason)), sessLabel),
		addDelta(sessIRI, predEndNode, quad.IRI(string(terminalNodeID)), sessLabel),
	)

	return s.qs.ApplyDeltas(deltas, graph.IgnoreOpts{IgnoreDup: true, IgnoreMissing: true})
}

// LoadGraph reconstructs an in-memory Graph from stored quads.
func (s *Store) LoadGraph(ctx context.Context, sessionID sgp.ID) (*sgp.Graph, error) {
	sess, status, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	// Get head node ID.
	headVals, _ := s.outValues(ctx, string(sessionID), predHead)
	var headID sgp.ID
	if len(headVals) > 0 {
		headID = sgp.ID(valToStr(headVals[0]))
	}

	// Get end_reason and end_node if closed.
	var reason sgp.EndReason
	var terminalNodeID sgp.ID
	if status == sgp.SessionStatusClosed {
		erVals, _ := s.outValues(ctx, string(sessionID), predEndReason)
		if len(erVals) > 0 {
			reason = sgp.EndReason(valToStr(erVals[0]))
		}
		enVals, _ := s.outValues(ctx, string(sessionID), predEndNode)
		if len(enVals) > 0 {
			terminalNodeID = sgp.ID(valToStr(enVals[0]))
		}
	}

	// Get all node IDs for this session.
	nodeIDVals, err := s.inValues(ctx, string(sessionID), predSession)
	if err != nil {
		return nil, fmt.Errorf("list node ids: %w", err)
	}

	nodes := make([]sgp.Node, 0, len(nodeIDVals))
	for _, v := range nodeIDVals {
		nodeID := sgp.ID(valToStr(v))
		node, err := s.GetNode(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("load node %s: %w", nodeID, err)
		}
		nodes = append(nodes, node)
	}

	return sgp.RestoreFromNodes(sess, nodes, headID, status, reason, terminalNodeID)
}

// GetNode fetches a single node by ID from the quad store.
func (s *Store) GetNode(ctx context.Context, nodeID sgp.ID) (sgp.Node, error) {
	sessVals, _ := s.outValues(ctx, string(nodeID), predSession)
	if len(sessVals) == 0 {
		return sgp.Node{}, fmt.Errorf("%w: %s", sgp.ErrNodeNotFound, nodeID)
	}

	tsVals, _ := s.outValues(ctx, string(nodeID), predTimestamp)
	msgVals, _ := s.outValues(ctx, string(nodeID), predMessageJSON)
	parentVals, _ := s.outValues(ctx, string(nodeID), predParent)
	synthVals, _ := s.outValues(ctx, string(nodeID), predSynthesizedFrom)

	var msg sgp.Message
	if len(msgVals) > 0 {
		_ = json.Unmarshal([]byte(valToStr(msgVals[0])), &msg)
	}

	parentIDs := make([]sgp.ID, 0, len(parentVals))
	for _, v := range parentVals {
		parentIDs = append(parentIDs, sgp.ID(valToStr(v)))
	}

	synthFrom := make([]sgp.ID, 0, len(synthVals))
	for _, v := range synthVals {
		synthFrom = append(synthFrom, sgp.ID(valToStr(v)))
	}

	var ts time.Time
	if len(tsVals) > 0 {
		ts = parseRFC3339(valToStr(tsVals[0]))
	}

	return sgp.Node{
		ID:              nodeID,
		SessionID:       sgp.ID(valToStr(sessVals[0])),
		Timestamp:       ts,
		ParentIDs:       parentIDs,
		SynthesizedFrom: synthFrom,
		Message:         msg,
	}, nil
}

// GetLineage returns the canonical ancestor chain from root to nodeID (inclusive).
func (s *Store) GetLineage(ctx context.Context, nodeID sgp.ID) ([]sgp.Node, error) {
	lineage := make([]sgp.Node, 0)
	current := nodeID

	for {
		node, err := s.GetNode(ctx, current)
		if err != nil {
			return nil, err
		}
		lineage = append(lineage, node)

		if len(node.ParentIDs) == 0 {
			break
		}
		current = node.ParentIDs[0] // canonical (first) parent
	}

	slices.Reverse(lineage)
	return lineage, nil
}

// GetSession returns session metadata and status.
func (s *Store) GetSession(ctx context.Context, sessionID sgp.ID) (sgp.Session, sgp.SessionStatus, error) {
	tsVals, _ := s.outValues(ctx, string(sessionID), predTimestamp)
	if len(tsVals) == 0 {
		return sgp.Session{}, 0, fmt.Errorf("%w: %s", sgp.ErrSessionNotFound, sessionID)
	}

	ts := parseRFC3339(valToStr(tsVals[0]))

	statusVals, _ := s.outValues(ctx, string(sessionID), predStatus)
	status := sgp.SessionStatusOpen
	if len(statusVals) > 0 && valToStr(statusVals[0]) == statusClosed {
		status = sgp.SessionStatusClosed
	}

	sess := sgp.Session{
		ID:        sessionID,
		Timestamp: ts,
	}

	sfSessVals, _ := s.outValues(ctx, string(sessionID), predSpawnedFromSession)
	sfNodeVals, _ := s.outValues(ctx, string(sessionID), predSpawnedFromNode)
	if len(sfSessVals) > 0 && len(sfNodeVals) > 0 {
		sess.SpawnedFrom = &sgp.SpawnReference{
			SessionID: sgp.ID(valToStr(sfSessVals[0])),
			NodeID:    sgp.ID(valToStr(sfNodeVals[0])),
		}
	}

	return sess, status, nil
}

// ListSessions returns sessions in ascending timestamp order with keyset pagination.
func (s *Store) ListSessions(ctx context.Context, cursor string, limit int) ([]sgp.Session, string, error) {
	if limit <= 0 {
		limit = 50
	}

	memberVals, err := s.outValues(ctx, globalSessions, predMember)
	if err != nil {
		return nil, "", fmt.Errorf("list sessions: %w", err)
	}

	sessions := make([]sgp.Session, 0, len(memberVals))
	for _, v := range memberVals {
		sessionID := sgp.ID(valToStr(v))
		sess, _, err := s.GetSession(ctx, sessionID)
		if err != nil {
			continue
		}
		sessions = append(sessions, sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Timestamp.Before(sessions[j].Timestamp)
	})

	// Apply cursor (skip up to and including cursor session).
	if cursor != "" {
		for i, sess := range sessions {
			if string(sess.ID) == cursor {
				sessions = sessions[i+1:]
				break
			}
		}
	}

	var nextCursor string
	if len(sessions) > limit {
		sessions = sessions[:limit]
		nextCursor = string(sessions[len(sessions)-1].ID)
	}

	return sessions, nextCursor, nil
}

// GetSessionGraph returns all nodes and edges for a session (extra method, not on sgp.Store).
func (s *Store) GetSessionGraph(ctx context.Context, sessionID sgp.ID) ([]sgp.Node, []Edge, error) {
	nodeIDVals, err := s.inValues(ctx, string(sessionID), predSession)
	if err != nil {
		return nil, nil, fmt.Errorf("list node ids: %w", err)
	}

	nodes := make([]sgp.Node, 0, len(nodeIDVals))
	var edges []Edge

	for _, v := range nodeIDVals {
		nodeID := sgp.ID(valToStr(v))
		node, err := s.GetNode(ctx, nodeID)
		if err != nil {
			return nil, nil, err
		}
		nodes = append(nodes, node)

		for _, pid := range node.ParentIDs {
			edges = append(edges, Edge{FromID: pid, ToID: nodeID, Kind: "parent"})
		}
		for _, sid := range node.SynthesizedFrom {
			edges = append(edges, Edge{FromID: sid, ToID: nodeID, Kind: "synthesized_from"})
		}
	}

	return nodes, edges, nil
}

// outValues returns all object values for quads with the given subject and predicate.
func (s *Store) outValues(ctx context.Context, subject, pred string) ([]quad.Value, error) {
	p := path.StartPath(s.qs, quad.IRI(subject)).Out(quad.IRI(pred))
	return p.Iterate(ctx).AllValues(s.qs)
}

// inValues returns all subject values for quads with the given object and predicate.
func (s *Store) inValues(ctx context.Context, object, pred string) ([]quad.Value, error) {
	p := path.StartPath(s.qs, quad.IRI(object)).In(quad.IRI(pred))
	return p.Iterate(ctx).AllValues(s.qs)
}
