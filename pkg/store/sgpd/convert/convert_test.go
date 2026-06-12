package convert_test

import (
	"testing"
	"time"

	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
)

func sessionEventCases(ts time.Time) []sgp.Event {
	return []sgp.Event{
		{
			Kind:      sgp.EventKindSessionStart,
			Event:     "session.start",
			SessionID: "sess-1",
			Timestamp: ts,
		},
		{
			Kind:      sgp.EventKindSessionStart,
			Event:     "session.start",
			SessionID: "sess-spawned",
			Timestamp: ts,
			SpawnedFrom: &sgp.SpawnReference{
				SessionID: "sess-parent",
				NodeID:    "node-parent",
			},
		},
		{
			Kind:           sgp.EventKindSessionEnded,
			Event:          "session.ended",
			SessionID:      "sess-1",
			Timestamp:      ts,
			TerminalNodeID: "node-5",
			Reason:         sgp.EndReasonComplete,
		},
		{
			Kind:           sgp.EventKindSessionEnded,
			Event:          "session.ended",
			SessionID:      "sess-1",
			Timestamp:      ts,
			TerminalNodeID: "node-5",
			Reason:         sgp.EndReasonFailed,
		},
	}
}

func nodeEventCases(ts time.Time) []sgp.Event {
	return []sgp.Event{
		{
			Kind:      sgp.EventKindNodeAppended,
			Event:     "node.appended",
			SessionID: "sess-1",
			Timestamp: ts,
			Node: &sgp.Node{
				ID:        "node-1",
				SessionID: "sess-1",
				Timestamp: ts,
				Message:   sgp.Message{System: &sgp.SystemMessage{Text: "hello"}},
			},
		},
		{
			Kind:      sgp.EventKindNodeAppended,
			Event:     "node.appended",
			SessionID: "sess-1",
			Timestamp: ts,
			Node: &sgp.Node{
				ID:        "node-2",
				SessionID: "sess-1",
				Timestamp: ts,
				ParentIDs: []sgp.ID{"node-1"},
				Message: sgp.Message{User: &sgp.UserMessage{
					Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "what is 2+2?"}}},
				}},
			},
		},
		{
			Kind:      sgp.EventKindNodeAppended,
			Event:     "node.appended",
			SessionID: "sess-1",
			Timestamp: ts,
			Node: &sgp.Node{
				ID:        "node-3",
				SessionID: "sess-1",
				Timestamp: ts,
				ParentIDs: []sgp.ID{"node-2"},
				Message: sgp.Message{Assistant: &sgp.AssistantMessage{
					Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: "4"}}},
					ToolCalls: []sgp.ToolCall{
						{ID: "tc-1", Name: "calc", Arguments: `{"expr":"2+2"}`},
					},
				}},
			},
		},
		{
			Kind:      sgp.EventKindNodeAppended,
			Event:     "node.appended",
			SessionID: "sess-1",
			Timestamp: ts,
			Node: &sgp.Node{
				ID:        "node-4",
				SessionID: "sess-1",
				Timestamp: ts,
				ParentIDs: []sgp.ID{"node-3"},
				Message: sgp.Message{Tool: &sgp.ToolMessage{
					ToolCallID: "tc-1",
					Name:       "calc",
					Parts:      []sgp.ContentPart{{Text: &sgp.TextPart{Text: "4"}}},
				}},
			},
		},
		{
			Kind:      sgp.EventKindHistoryRewritten,
			Event:     "history.rewritten",
			SessionID: "sess-1",
			Timestamp: ts,
			Node: &sgp.Node{
				ID:              "node-5",
				SessionID:       "sess-1",
				Timestamp:       ts,
				ParentIDs:       []sgp.ID{"node-1"},
				SynthesizedFrom: []sgp.ID{"node-2", "node-3", "node-4"},
				Message:         sgp.Message{Assistant: &sgp.AssistantMessage{Refusal: "refused"}},
			},
		},
	}
}

func TestEventRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Now().UTC().Truncate(time.Millisecond)

	cases := append(sessionEventCases(ts), nodeEventCases(ts)...)

	for _, original := range cases {
		pb := convert.EventToProto(original)
		got := convert.EventFromProto(pb)
		assertEventEqual(t, original, got)
	}
}

func assertEventEqual(t *testing.T, original, got sgp.Event) {
	t.Helper()

	if got.Event != original.Event {
		t.Errorf("Event: got %q want %q", got.Event, original.Event)
	}

	if got.SessionID != original.SessionID {
		t.Errorf("SessionID: got %q want %q", got.SessionID, original.SessionID)
	}

	if !got.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp: got %v want %v", got.Timestamp, original.Timestamp)
	}

	if got.TerminalNodeID != original.TerminalNodeID {
		t.Errorf("TerminalNodeID: got %q want %q", got.TerminalNodeID, original.TerminalNodeID)
	}

	if got.Reason != original.Reason {
		t.Errorf("Reason: got %q want %q", got.Reason, original.Reason)
	}

	if got.Kind != original.Kind {
		t.Errorf("Kind: got %d want %d", got.Kind, original.Kind)
	}

	assertSpawnedFromEqual(t, original.SpawnedFrom, got.SpawnedFrom)
	assertEventNodeEqual(t, original.Event, original.Node, got.Node)
}

func assertSpawnedFromEqual(t *testing.T, original, got *sgp.SpawnReference) {
	t.Helper()

	if (got == nil) != (original == nil) {
		t.Error("SpawnedFrom nil mismatch")

		return
	}

	if original == nil {
		return
	}

	if got.SessionID != original.SessionID {
		t.Errorf("SpawnedFrom.SessionID: got %q want %q", got.SessionID, original.SessionID)
	}

	if got.NodeID != original.NodeID {
		t.Errorf("SpawnedFrom.NodeID: got %q want %q", got.NodeID, original.NodeID)
	}
}

func assertEventNodeEqual(t *testing.T, eventName string, original, got *sgp.Node) {
	t.Helper()

	if (got == nil) != (original == nil) {
		t.Errorf("Node nil mismatch for event %q", eventName)

		return
	}

	if original != nil {
		assertNodeEqual(t, *original, *got)
	}
}

func TestNodeRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Now().UTC().Truncate(time.Millisecond)
	original := sgp.Node{
		ID:              "node-x",
		SessionID:       "sess-x",
		Timestamp:       ts,
		ParentIDs:       []sgp.ID{"p1", "p2"},
		SynthesizedFrom: []sgp.ID{"s1"},
		Message: sgp.Message{User: &sgp.UserMessage{
			Parts: []sgp.ContentPart{
				{Text: &sgp.TextPart{Text: "hi"}},
				{
					Image: &sgp.ImagePart{
						BlobPart: sgp.BlobPart{MimeType: "image/png", Data: []byte{1, 2, 3}},
					},
				},
				{
					Audio: &sgp.AudioPart{
						BlobPart: sgp.BlobPart{MimeType: "audio/mp3", Data: []byte{4, 5}},
					},
				},
				{
					Video: &sgp.VideoPart{
						BlobPart: sgp.BlobPart{MimeType: "video/mp4", Data: []byte{6}},
					},
				},
				{
					File: &sgp.FilePart{
						BlobPart: sgp.BlobPart{MimeType: "application/pdf", Data: []byte{7}},
						Name:     "doc.pdf",
					},
				},
			},
		}},
	}
	pb := convert.NodeToProto(original)
	got := convert.NodeFromProto(pb)
	assertNodeEqual(t, original, got)
}

func TestSessionRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Now().UTC().Truncate(time.Millisecond)
	original := sgp.Session{
		ID:        "sess-1",
		Timestamp: ts,
		SpawnedFrom: &sgp.SpawnReference{
			SessionID: "parent-sess",
			NodeID:    "parent-node",
		},
	}
	pb := convert.SessionToProto(original)
	got := convert.SessionFromProto(pb)

	if got.ID != original.ID {
		t.Errorf("ID: got %q want %q", got.ID, original.ID)
	}

	if !got.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp: got %v want %v", got.Timestamp, original.Timestamp)
	}

	if got.SpawnedFrom == nil {
		t.Fatal("SpawnedFrom is nil")
	}

	if got.SpawnedFrom.SessionID != original.SpawnedFrom.SessionID {
		t.Errorf(
			"SpawnedFrom.SessionID: got %q want %q",
			got.SpawnedFrom.SessionID,
			original.SpawnedFrom.SessionID,
		)
	}
}

func assertNodeEqual(t *testing.T, want, got sgp.Node) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("Node.ID: got %q want %q", got.ID, want.ID)
	}

	if got.SessionID != want.SessionID {
		t.Errorf("Node.SessionID: got %q want %q", got.SessionID, want.SessionID)
	}

	if !got.Timestamp.Equal(want.Timestamp) {
		t.Errorf("Node.Timestamp: got %v want %v", got.Timestamp, want.Timestamp)
	}

	if len(got.ParentIDs) != len(want.ParentIDs) {
		t.Errorf("Node.ParentIDs len: got %d want %d", len(got.ParentIDs), len(want.ParentIDs))
	}

	if len(got.SynthesizedFrom) != len(want.SynthesizedFrom) {
		t.Errorf(
			"Node.SynthesizedFrom len: got %d want %d",
			len(got.SynthesizedFrom),
			len(want.SynthesizedFrom),
		)
	}

	if got.Message.Role() != want.Message.Role() {
		t.Errorf("Node.Message.Role: got %q want %q", got.Message.Role(), want.Message.Role())
	}
}
