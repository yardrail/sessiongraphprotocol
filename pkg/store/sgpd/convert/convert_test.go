package convert_test

import (
	"testing"
	"time"

	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd/convert"
	"google.golang.org/protobuf/types/known/timestamppb"
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
				Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindParent, NodeID: "node-1"}},
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
				Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindParent, NodeID: "node-2"}},
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
				Edges:     []sgp.EdgeRef{{Kind: sgp.EdgeKindParent, NodeID: "node-3"}},
				Message: sgp.Message{Tool: &sgp.ToolMessage{
					ToolCallID: "tc-1",
					Name:       "calc",
					Parts:      []sgp.ContentPart{{Text: &sgp.TextPart{Text: "4"}}},
				}},
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
		ID:        "node-x",
		SessionID: "sess-x",
		Timestamp: ts,
		Edges: []sgp.EdgeRef{
			{Kind: sgp.EdgeKindParent, NodeID: "p1"},
			{Kind: sgp.EdgeKindParent, NodeID: "p2"},
		},
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

	if len(got.Parents()) != len(want.Parents()) {
		t.Errorf("Node.Parents len: got %d want %d", len(got.Parents()), len(want.Parents()))
	}

	if got.Message.Role() != want.Message.Role() {
		t.Errorf("Node.Message.Role: got %q want %q", got.Message.Role(), want.Message.Role())
	}
}

// TestEventFromProtoNil covers the nil branch of EventFromProto.
func TestEventFromProtoNil(t *testing.T) {
	t.Parallel()

	got := convert.EventFromProto(nil)
	if got.Event != "" || got.SessionID != "" {
		t.Errorf("expected zero Event, got %+v", got)
	}
}

// TestNodeFromProtoNil covers the nil branch of NodeFromProto.
func TestNodeFromProtoNil(t *testing.T) {
	t.Parallel()

	got := convert.NodeFromProto(nil)
	if got.ID != "" || got.SessionID != "" {
		t.Errorf("expected zero Node, got %+v", got)
	}
}

// TestMessageFromProtoNil covers the nil branch of MessageFromProto.
func TestMessageFromProtoNil(t *testing.T) {
	t.Parallel()

	got := convert.MessageFromProto(nil)
	if got.System != nil || got.User != nil || got.Assistant != nil || got.Tool != nil {
		t.Errorf("expected zero Message, got %+v", got)
	}
}

// TestSessionFromProtoNil covers the nil branch of SessionFromProto.
func TestSessionFromProtoNil(t *testing.T) {
	t.Parallel()

	got := convert.SessionFromProto(nil)
	if got.ID != "" {
		t.Errorf("expected zero Session, got %+v", got)
	}
}

// TestMessageToProtoDefaultBranch covers the default (zero) branch of MessageToProto.
func TestMessageToProtoDefaultBranch(t *testing.T) {
	t.Parallel()

	got := convert.MessageToProto(sgp.Message{})
	if got != nil {
		t.Errorf("expected nil proto for zero Message, got %+v", got)
	}
}

// TestContentPartsToProtoEmpty covers the empty-slice branch of contentPartsToProto
// via NodeToProto with a zero Message (no parts).
func TestContentPartsToProtoEmpty(t *testing.T) {
	t.Parallel()

	n := sgp.Node{
		ID:        "n1",
		SessionID: "s1",
		Message:   sgp.Message{User: &sgp.UserMessage{Parts: nil}},
	}
	pb := convert.NodeToProto(n)
	if pb == nil {
		t.Fatal("expected non-nil proto Node")
	}
	// Message should have user with nil parts — round-trip check
	got := convert.NodeFromProto(pb)
	if got.Message.User == nil {
		t.Fatal("expected User message after round-trip")
	}
	if got.Message.User.Parts != nil {
		t.Errorf("expected nil parts, got %v", got.Message.User.Parts)
	}
}

// TestTimeFromProtoNil covers the nil branch of TimeFromProto.
func TestTimeFromProtoNil(t *testing.T) {
	t.Parallel()

	got := convert.TimeFromProto(nil)
	if !got.IsZero() {
		t.Errorf("expected zero time, got %v", got)
	}
}

// TestTimeFromProtoNonNil covers the non-nil branch of TimeFromProto.
func TestTimeFromProtoNonNil(t *testing.T) {
	t.Parallel()

	now := time.Now()
	ts := timestamppb.New(now)
	got := convert.TimeFromProto(ts)
	if got.IsZero() {
		t.Error("expected non-zero time")
	}
}

// TestContentPartsFromProtoNil covers the nil-slice branch of contentPartsFromProto
// by calling MessageFromProto with a User message that has no parts.
func TestContentPartsFromProtoNil(t *testing.T) {
	t.Parallel()

	pb := &sgpv1.Message{Message: &sgpv1.Message_User{
		User: &sgpv1.UserMessage{Parts: nil},
	}}
	got := convert.MessageFromProto(pb)
	if got.User == nil {
		t.Fatal("expected User message")
	}
	if got.User.Parts != nil {
		t.Errorf("expected nil parts, got %v", got.User.Parts)
	}
}

// TestContentPartsFromProtoEmpty covers the empty-slice branch of contentPartsFromProto.
func TestContentPartsFromProtoEmpty(t *testing.T) {
	t.Parallel()

	pb := &sgpv1.Message{Message: &sgpv1.Message_User{
		User: &sgpv1.UserMessage{Parts: []*sgpv1.ContentPart{}},
	}}
	got := convert.MessageFromProto(pb)
	if got.User == nil {
		t.Fatal("expected User message")
	}
	if got.User.Parts != nil {
		t.Errorf("expected nil parts, got %v", got.User.Parts)
	}
}

// TestContentPartFromProtoNil covers the nil branch of contentPartFromProto
// by including a nil element in the parts slice.
func TestContentPartFromProtoNil(t *testing.T) {
	t.Parallel()

	pb := &sgpv1.Message{Message: &sgpv1.Message_User{
		User: &sgpv1.UserMessage{Parts: []*sgpv1.ContentPart{nil}},
	}}
	got := convert.MessageFromProto(pb)
	if got.User == nil {
		t.Fatal("expected User message")
	}
	if len(got.User.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(got.User.Parts))
	}
	// nil ContentPart should produce empty ContentPart
	p := got.User.Parts[0]
	if p.Text != nil || p.Image != nil || p.Audio != nil || p.Video != nil || p.File != nil {
		t.Errorf("expected empty ContentPart, got %+v", p)
	}
}

// TestBlobFromProtoNil covers the nil branch of blobFromProto by providing an
// ImagePart with no blob set, so GetBlob() returns nil.
func TestBlobFromProtoNil(t *testing.T) {
	t.Parallel()

	pb := &sgpv1.Message{Message: &sgpv1.Message_User{
		User: &sgpv1.UserMessage{Parts: []*sgpv1.ContentPart{
			{Part: &sgpv1.ContentPart_Image{Image: &sgpv1.ImagePart{Blob: nil}}},
		}},
	}}
	got := convert.MessageFromProto(pb)
	if got.User == nil {
		t.Fatal("expected User message")
	}
	if len(got.User.Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(got.User.Parts))
	}
	p := got.User.Parts[0]
	if p.Image == nil {
		t.Fatal("expected Image part")
	}
	// nil blob → zero BlobPart
	if p.Image.BlobPart.MimeType != "" || p.Image.BlobPart.Data != nil {
		t.Errorf("expected zero BlobPart, got %+v", p.Image.BlobPart)
	}
}

// TestToolCallFromProtoNil covers the nil branch of toolCallFromProto by
// including a nil element in the ToolCalls slice.
func TestToolCallFromProtoNil(t *testing.T) {
	t.Parallel()

	pb := &sgpv1.Message{Message: &sgpv1.Message_Assistant{
		Assistant: &sgpv1.AssistantMessage{
			ToolCalls: []*sgpv1.ToolCall{nil},
		},
	}}
	got := convert.MessageFromProto(pb)
	if got.Assistant == nil {
		t.Fatal("expected Assistant message")
	}
	if len(got.Assistant.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(got.Assistant.ToolCalls))
	}
	// nil ToolCall → zero ToolCall
	tc := got.Assistant.ToolCalls[0]
	if tc.ID != "" || tc.Name != "" || tc.Arguments != "" {
		t.Errorf("expected zero ToolCall, got %+v", tc)
	}
}
