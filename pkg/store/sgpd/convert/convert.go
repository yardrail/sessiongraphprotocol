// Package convert translates between SGP domain types and their protobuf representations.
package convert

import (
	"time"

	sgpv1 "github.com/restrukt-ai/sessiongraphprotocol/gen/sgp/v1"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventToProto converts an SGP Event to its protobuf representation.
func EventToProto(e sgp.Event) *sgpv1.Event {
	pb := &sgpv1.Event{
		Event:          e.Event,
		SessionId:      string(e.SessionID),
		Timestamp:      timestamppb.New(e.Timestamp),
		TerminalNodeId: string(e.TerminalNodeID),
		Reason:         endReasonToProto(e.Reason),
	}
	if e.SpawnedFrom != nil {
		pb.SpawnedFrom = spawnRefToProto(e.SpawnedFrom)
	}

	if e.Node != nil {
		pb.Node = NodeToProto(*e.Node)
	}

	return pb
}

// EventFromProto converts a protobuf Event to its SGP domain representation.
func EventFromProto(pb *sgpv1.Event) sgp.Event {
	if pb == nil {
		return sgp.Event{}
	}

	e := sgp.Event{
		Event:          pb.GetEvent(),
		SessionID:      sgp.ID(pb.GetSessionId()),
		TerminalNodeID: sgp.ID(pb.GetTerminalNodeId()),
		Reason:         endReasonFromProto(pb.GetReason()),
	}
	if pb.GetTimestamp() != nil {
		e.Timestamp = pb.GetTimestamp().AsTime()
	}

	if pb.GetSpawnedFrom() != nil {
		ref := spawnRefFromProto(pb.GetSpawnedFrom())
		e.SpawnedFrom = &ref
	}

	if pb.GetNode() != nil {
		n := NodeFromProto(pb.GetNode())
		e.Node = &n
	}

	e.Kind = sgp.ClassifyEvent(e)

	return e
}

// NodeToProto converts an SGP Node to its protobuf representation.
func NodeToProto(n sgp.Node) *sgpv1.Node {
	pb := &sgpv1.Node{
		Id:        string(n.ID),
		SessionId: string(n.SessionID),
		Timestamp: timestamppb.New(n.Timestamp),
		Message:   MessageToProto(n.Message),
	}
	for _, id := range n.ParentIDs {
		pb.ParentIds = append(pb.ParentIds, string(id))
	}

	for _, id := range n.SynthesizedFrom {
		pb.SynthesizedFrom = append(pb.SynthesizedFrom, string(id))
	}

	return pb
}

// NodeFromProto converts a protobuf Node to its SGP domain representation.
func NodeFromProto(pb *sgpv1.Node) sgp.Node {
	if pb == nil {
		return sgp.Node{}
	}

	n := sgp.Node{
		ID:        sgp.ID(pb.GetId()),
		SessionID: sgp.ID(pb.GetSessionId()),
		Message:   MessageFromProto(pb.GetMessage()),
	}
	if pb.GetTimestamp() != nil {
		n.Timestamp = pb.GetTimestamp().AsTime()
	}

	for _, id := range pb.GetParentIds() {
		n.ParentIDs = append(n.ParentIDs, sgp.ID(id))
	}

	for _, id := range pb.GetSynthesizedFrom() {
		n.SynthesizedFrom = append(n.SynthesizedFrom, sgp.ID(id))
	}

	return n
}

// MessageToProto converts an SGP Message to its protobuf representation.
func MessageToProto(m sgp.Message) *sgpv1.Message {
	switch {
	case m.System != nil:
		return &sgpv1.Message{Message: &sgpv1.Message_System{
			System: &sgpv1.SystemMessage{Text: m.System.Text},
		}}
	case m.User != nil:
		return &sgpv1.Message{Message: &sgpv1.Message_User{
			User: &sgpv1.UserMessage{Parts: contentPartsToProto(m.User.Parts)},
		}}
	case m.Assistant != nil:
		am := &sgpv1.AssistantMessage{
			Parts:   contentPartsToProto(m.Assistant.Parts),
			Refusal: m.Assistant.Refusal,
		}
		for _, tc := range m.Assistant.ToolCalls {
			am.ToolCalls = append(am.ToolCalls, toolCallToProto(tc))
		}

		return &sgpv1.Message{Message: &sgpv1.Message_Assistant{Assistant: am}}
	case m.Tool != nil:
		return &sgpv1.Message{Message: &sgpv1.Message_Tool{
			Tool: &sgpv1.ToolMessage{
				ToolCallId: m.Tool.ToolCallID,
				Name:       m.Tool.Name,
				Parts:      contentPartsToProto(m.Tool.Parts),
				IsError:    m.Tool.IsError,
			},
		}}
	default:
		return nil
	}
}

// MessageFromProto converts a protobuf Message to its SGP domain representation.
func MessageFromProto(pb *sgpv1.Message) sgp.Message {
	if pb == nil {
		return sgp.Message{}
	}

	switch v := pb.GetMessage().(type) {
	case *sgpv1.Message_System:
		return sgp.Message{System: &sgp.SystemMessage{Text: v.System.GetText()}}
	case *sgpv1.Message_User:
		return sgp.Message{User: &sgp.UserMessage{Parts: contentPartsFromProto(v.User.GetParts())}}
	case *sgpv1.Message_Assistant:
		am := &sgp.AssistantMessage{
			Parts:   contentPartsFromProto(v.Assistant.GetParts()),
			Refusal: v.Assistant.GetRefusal(),
		}
		for _, tc := range v.Assistant.GetToolCalls() {
			am.ToolCalls = append(am.ToolCalls, toolCallFromProto(tc))
		}

		return sgp.Message{Assistant: am}
	case *sgpv1.Message_Tool:
		return sgp.Message{Tool: &sgp.ToolMessage{
			ToolCallID: v.Tool.GetToolCallId(),
			Name:       v.Tool.GetName(),
			Parts:      contentPartsFromProto(v.Tool.GetParts()),
			IsError:    v.Tool.GetIsError(),
		}}
	default:
		return sgp.Message{}
	}
}

// SessionToProto converts an SGP Session to its protobuf representation.
func SessionToProto(s sgp.Session) *sgpv1.Session {
	pb := &sgpv1.Session{
		Id:        string(s.ID),
		Timestamp: timestamppb.New(s.Timestamp),
	}
	if s.SpawnedFrom != nil {
		pb.SpawnedFrom = spawnRefToProto(s.SpawnedFrom)
	}

	return pb
}

// SessionFromProto converts a protobuf Session to its SGP domain representation.
func SessionFromProto(pb *sgpv1.Session) sgp.Session {
	if pb == nil {
		return sgp.Session{}
	}

	s := sgp.Session{ID: sgp.ID(pb.GetId())}
	if pb.GetTimestamp() != nil {
		s.Timestamp = pb.GetTimestamp().AsTime()
	}

	if pb.GetSpawnedFrom() != nil {
		ref := spawnRefFromProto(pb.GetSpawnedFrom())
		s.SpawnedFrom = &ref
	}

	return s
}

func contentPartsToProto(parts []sgp.ContentPart) []*sgpv1.ContentPart {
	if len(parts) == 0 {
		return nil
	}

	out := make([]*sgpv1.ContentPart, 0, len(parts))

	for _, p := range parts {
		out = append(out, contentPartToProto(p))
	}

	return out
}

func contentPartToProto(p sgp.ContentPart) *sgpv1.ContentPart {
	switch {
	case p.Text != nil:
		return &sgpv1.ContentPart{Part: &sgpv1.ContentPart_Text{
			Text: &sgpv1.TextPart{Text: p.Text.Text},
		}}
	case p.Image != nil:
		return &sgpv1.ContentPart{Part: &sgpv1.ContentPart_Image{
			Image: &sgpv1.ImagePart{Blob: blobToProto(p.Image.BlobPart)},
		}}
	case p.Audio != nil:
		return &sgpv1.ContentPart{Part: &sgpv1.ContentPart_Audio{
			Audio: &sgpv1.AudioPart{Blob: blobToProto(p.Audio.BlobPart)},
		}}
	case p.Video != nil:
		return &sgpv1.ContentPart{Part: &sgpv1.ContentPart_Video{
			Video: &sgpv1.VideoPart{Blob: blobToProto(p.Video.BlobPart)},
		}}
	case p.File != nil:
		return &sgpv1.ContentPart{Part: &sgpv1.ContentPart_File{
			File: &sgpv1.FilePart{Blob: blobToProto(p.File.BlobPart), Name: p.File.Name},
		}}
	default:
		return &sgpv1.ContentPart{}
	}
}

func contentPartsFromProto(pbs []*sgpv1.ContentPart) []sgp.ContentPart {
	if len(pbs) == 0 {
		return nil
	}

	out := make([]sgp.ContentPart, 0, len(pbs))

	for _, pb := range pbs {
		out = append(out, contentPartFromProto(pb))
	}

	return out
}

func contentPartFromProto(pb *sgpv1.ContentPart) sgp.ContentPart {
	if pb == nil {
		return sgp.ContentPart{}
	}

	switch v := pb.GetPart().(type) {
	case *sgpv1.ContentPart_Text:
		return sgp.ContentPart{Text: &sgp.TextPart{Text: v.Text.GetText()}}
	case *sgpv1.ContentPart_Image:
		return sgp.ContentPart{Image: &sgp.ImagePart{BlobPart: blobFromProto(v.Image.GetBlob())}}
	case *sgpv1.ContentPart_Audio:
		return sgp.ContentPart{Audio: &sgp.AudioPart{BlobPart: blobFromProto(v.Audio.GetBlob())}}
	case *sgpv1.ContentPart_Video:
		return sgp.ContentPart{Video: &sgp.VideoPart{BlobPart: blobFromProto(v.Video.GetBlob())}}
	case *sgpv1.ContentPart_File:
		return sgp.ContentPart{
			File: &sgp.FilePart{BlobPart: blobFromProto(v.File.GetBlob()), Name: v.File.GetName()},
		}
	default:
		return sgp.ContentPart{}
	}
}

func blobToProto(b sgp.BlobPart) *sgpv1.BlobPart {
	return &sgpv1.BlobPart{MimeType: b.MimeType, Data: b.Data}
}

func blobFromProto(pb *sgpv1.BlobPart) sgp.BlobPart {
	if pb == nil {
		return sgp.BlobPart{}
	}

	return sgp.BlobPart{MimeType: pb.GetMimeType(), Data: pb.GetData()}
}

func toolCallToProto(tc sgp.ToolCall) *sgpv1.ToolCall {
	return &sgpv1.ToolCall{Id: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
}

func toolCallFromProto(pb *sgpv1.ToolCall) sgp.ToolCall {
	if pb == nil {
		return sgp.ToolCall{}
	}

	return sgp.ToolCall{ID: pb.GetId(), Name: pb.GetName(), Arguments: pb.GetArguments()}
}

func spawnRefToProto(ref *sgp.SpawnReference) *sgpv1.SpawnReference {
	if ref == nil {
		return nil
	}

	return &sgpv1.SpawnReference{SessionId: string(ref.SessionID), NodeId: string(ref.NodeID)}
}

func spawnRefFromProto(pb *sgpv1.SpawnReference) sgp.SpawnReference {
	if pb == nil {
		return sgp.SpawnReference{}
	}

	return sgp.SpawnReference{SessionID: sgp.ID(pb.GetSessionId()), NodeID: sgp.ID(pb.GetNodeId())}
}

func endReasonToProto(r sgp.EndReason) sgpv1.EndReason {
	switch r {
	case sgp.EndReasonComplete:
		return sgpv1.EndReason_END_REASON_COMPLETE
	case sgp.EndReasonFailed:
		return sgpv1.EndReason_END_REASON_FAILED
	default:
		return sgpv1.EndReason_END_REASON_UNSPECIFIED
	}
}

func endReasonFromProto(pb sgpv1.EndReason) sgp.EndReason {
	switch pb {
	case sgpv1.EndReason_END_REASON_COMPLETE:
		return sgp.EndReasonComplete
	case sgpv1.EndReason_END_REASON_FAILED:
		return sgp.EndReasonFailed
	default:
		return ""
	}
}

// TimeFromProto converts a protobuf timestamp to [time.Time], returning zero if nil.
func TimeFromProto(ts interface{ AsTime() time.Time }) time.Time {
	if ts == nil {
		return time.Time{}
	}

	return ts.AsTime()
}
