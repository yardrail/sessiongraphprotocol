package cayleystore

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/cayleygraph/cayley/graph"
	"github.com/cayleygraph/quad"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

// Predicate constants — all use the "sgp:" namespace prefix.
const (
	predSession            = "sgp:session"
	predTimestamp          = "sgp:timestamp"
	predMessageJSON        = "sgp:message_json"
	predStatus  = "sgp:status"
	predHead               = "sgp:head"
	predEndReason          = "sgp:end_reason"
	predEndNode            = "sgp:end_node"
	predSpawnedFromSession = "sgp:spawned_from_session"
	predSpawnedFromNode    = "sgp:spawned_from_node"
	predMember             = "sgp:member"
	// typed node predicates
	predNodeKind           = "sgp:node_kind"
	predArchived           = "sgp:archived"
	predContentJSON        = "sgp:content_json"
	predEdgeParent         = "sgp:edge_parent"
	predEdgeDistilledFrom  = "sgp:edge_distilled_from"
	predEdgeAssociatedWith = "sgp:edge_associated_with"
	predEdgeRecalledIn     = "sgp:edge_recalled_in"
	predEdgeEvolvedFrom    = "sgp:edge_evolved_from"
	predEdgeProceduralOf   = "sgp:edge_procedural_of"
	predEdgeArchives       = "sgp:edge_archives"
	predEdgeBranchFrom     = "sgp:edge_branch_from"
	predEdgeWeight         = "sgp:edge_weight"

	globalSessions = "sgp:sessions"
	globalLabel    = "sgp:global"

	statusOpen   = "open"
	statusClosed = "closed"
)

// edgeKindToPred maps an EdgeKind to its quad predicate string.
func edgeKindToPred(kind sgp.EdgeKind) string {
	switch kind {
	case sgp.EdgeKindParent:
		return predEdgeParent
	case sgp.EdgeKindDistilledFrom:
		return predEdgeDistilledFrom
	case sgp.EdgeKindAssociatedWith:
		return predEdgeAssociatedWith
	case sgp.EdgeKindRecalledIn:
		return predEdgeRecalledIn
	case sgp.EdgeKindEvolvedFrom:
		return predEdgeEvolvedFrom
	case sgp.EdgeKindProceduralOf:
		return predEdgeProceduralOf
	case sgp.EdgeKindArchives:
		return predEdgeArchives
	case sgp.EdgeKindBranchFrom:
		return predEdgeBranchFrom
	default:
		return ""
	}
}

// isWeightedEdge reports whether an edge kind uses reified weighted storage.
func isWeightedEdge(kind sgp.EdgeKind) bool {
	return kind == sgp.EdgeKindAssociatedWith || kind == sgp.EdgeKindRecalledIn
}

// nodeToDeltas returns Add deltas for all quads representing node.
func nodeToDeltas(node sgp.Node) []graph.Delta {
	subj := quad.IRI(string(node.ID))
	label := quad.IRI(string(node.SessionID))

	msgJSON, _ := json.Marshal(node.Message)

	deltas := []graph.Delta{
		addDelta(subj, predSession, quad.IRI(string(node.SessionID)), label),
		addDelta(subj, predTimestamp, quad.String(node.Timestamp.UTC().Format(time.RFC3339Nano)), label),
		addDelta(subj, predMessageJSON, quad.String(string(msgJSON)), label),
	}

	// Write node kind if set.
	if node.Kind != "" {
		deltas = append(deltas, addDelta(subj, predNodeKind, quad.String(string(node.Kind)), label))
	}

	// Write archived flag if set.
	if node.Archived {
		deltas = append(deltas, addDelta(subj, predArchived, quad.String("true"), label))
	}

	// Write content JSON if any content field is non-nil.
	if contentJSON := marshalNodeContent(node); contentJSON != "" {
		deltas = append(deltas, addDelta(subj, predContentJSON, quad.String(contentJSON), label))
	}

	// Write typed edges.
	for _, e := range node.Edges {
		pred := edgeKindToPred(e.Kind)
		if pred == "" {
			continue
		}
		if isWeightedEdge(e.Kind) && e.Weight != 0 {
			// Reified weighted edge: store as edge IRI with weight quad.
			edgeIRI := quad.IRI(fmt.Sprintf("edge:%s:%s:%s", node.ID, e.Kind, e.NodeID))
			deltas = append(deltas,
				addDelta(subj, pred, edgeIRI, label),
				addDelta(edgeIRI, predEdgeWeight, quad.String(strconv.FormatFloat(e.Weight, 'f', -1, 64)), label),
			)
		} else {
			deltas = append(deltas, addDelta(subj, pred, quad.IRI(string(e.NodeID)), label))
		}
	}

	return deltas
}

// marshalNodeContent marshals whichever content field is non-nil.
// Returns empty string if none are set.
func marshalNodeContent(node sgp.Node) string {
	var v interface{}
	switch {
	case node.Memory != nil:
		v = node.Memory
	case node.Skill != nil:
		v = node.Skill
	case node.Identity != nil:
		v = node.Identity
	case node.Sleep != nil:
		v = node.Sleep
	default:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// sessionToDeltas returns Add deltas for session metadata (status="open").
func sessionToDeltas(sess sgp.Session) []graph.Delta {
	subj := quad.IRI(string(sess.ID))
	label := quad.IRI(string(sess.ID))

	deltas := []graph.Delta{
		addDelta(subj, predTimestamp, quad.String(sess.Timestamp.UTC().Format(time.RFC3339Nano)), label),
		addDelta(subj, predStatus, quad.String(statusOpen), label),
	}

	if sess.SpawnedFrom != nil {
		deltas = append(deltas,
			addDelta(subj, predSpawnedFromSession, quad.IRI(string(sess.SpawnedFrom.SessionID)), label),
			addDelta(subj, predSpawnedFromNode, quad.IRI(string(sess.SpawnedFrom.NodeID)), label),
		)
	}

	return deltas
}

// addDelta returns a graph.Delta that adds a quad.
func addDelta(subj quad.IRI, pred string, obj quad.Value, label quad.IRI) graph.Delta {
	return graph.Delta{
		Quad:   quad.Make(subj, quad.IRI(pred), obj, label),
		Action: graph.Add,
	}
}

// delDelta returns a graph.Delta that deletes a quad.
func delDelta(subj quad.IRI, pred string, obj quad.Value, label quad.IRI) graph.Delta {
	return graph.Delta{
		Quad:   quad.Make(subj, quad.IRI(pred), obj, label),
		Action: graph.Delete,
	}
}

// valToStr converts a quad.Value to its string representation.
// IRI values are returned as-is; String literals are unboxed.
func valToStr(v quad.Value) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case quad.IRI:
		return string(x)
	case quad.String:
		return string(x)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// parseRFC3339 parses an RFC3339Nano timestamp, returning zero on failure.
func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
