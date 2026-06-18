package cayleystore

import (
	"encoding/json"
	"fmt"
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
	predParent             = "sgp:parent"
	predSynthesizedFrom    = "sgp:synthesized_from"
	predStatus             = "sgp:status"
	predHead               = "sgp:head"
	predEndReason          = "sgp:end_reason"
	predEndNode            = "sgp:end_node"
	predSpawnedFromSession = "sgp:spawned_from_session"
	predSpawnedFromNode    = "sgp:spawned_from_node"
	predMember             = "sgp:member"

	globalSessions = "sgp:sessions"
	globalLabel    = "sgp:global"

	statusOpen   = "open"
	statusClosed = "closed"
)

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

	for _, pid := range node.ParentIDs {
		deltas = append(deltas, addDelta(subj, predParent, quad.IRI(string(pid)), label))
	}
	for _, sid := range node.SynthesizedFrom {
		deltas = append(deltas, addDelta(subj, predSynthesizedFrom, quad.IRI(string(sid)), label))
	}

	return deltas
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
