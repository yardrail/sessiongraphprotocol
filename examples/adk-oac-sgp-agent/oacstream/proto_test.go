package oacstream

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestMarshalHarnessEnvelope(t *testing.T) {
	t.Parallel()

	data, err := MarshalHarnessEnvelope(HarnessEnvelope{
		SessionID: "2a1ed485-9e5b-4998-b3c8-b435604df3a8",
		Result: &EventResult{
			Success:      false,
			ErrorMessage: "failed",
		},
	})
	if err != nil {
		t.Fatalf("MarshalHarnessEnvelope() error = %v", err)
	}

	if len(data) == 0 {
		t.Fatalf("MarshalHarnessEnvelope() returned empty payload")
	}
}

func TestUnmarshalOrchestratorEnvelopeEvent(t *testing.T) {
	t.Parallel()

	eventMsg := make([]byte, 0, 64)
	eventMsg = protowire.AppendTag(eventMsg, fieldEventChannel, protowire.BytesType)
	eventMsg = protowire.AppendString(eventMsg, "agent.events")
	eventMsg = protowire.AppendTag(eventMsg, fieldEventPayload, protowire.BytesType)
	eventMsg = protowire.AppendBytes(eventMsg, []byte("hello"))
	eventMsg = protowire.AppendTag(eventMsg, fieldEventContentType, protowire.BytesType)
	eventMsg = protowire.AppendString(eventMsg, "text/plain")

	envelopeMsg := make([]byte, 0, 96)
	envelopeMsg = protowire.AppendTag(envelopeMsg, fieldSessionID, protowire.BytesType)
	envelopeMsg = protowire.AppendString(envelopeMsg, "f5be33cc-6f3d-4e4c-b642-2ce94f4f3f07")
	envelopeMsg = protowire.AppendTag(envelopeMsg, fieldBodyEvent, protowire.BytesType)
	envelopeMsg = protowire.AppendBytes(envelopeMsg, eventMsg)

	envelope, err := UnmarshalOrchestratorEnvelope(envelopeMsg)
	if err != nil {
		t.Fatalf("UnmarshalOrchestratorEnvelope() error = %v", err)
	}
	if envelope.Event == nil {
		t.Fatalf("expected event body")
	}
	if envelope.Event.Channel != "agent.events" {
		t.Fatalf("channel = %s, want agent.events", envelope.Event.Channel)
	}
	if string(envelope.Event.Payload) != "hello" {
		t.Fatalf("payload = %q, want hello", string(envelope.Event.Payload))
	}
	if envelope.Event.ContentType != "text/plain" {
		t.Fatalf("content_type = %s, want text/plain", envelope.Event.ContentType)
	}
}

func TestUnmarshalOrchestratorEnvelopeSessionEnd(t *testing.T) {
	t.Parallel()

	envelopeMsg := make([]byte, 0, 64)
	envelopeMsg = protowire.AppendTag(envelopeMsg, fieldSessionID, protowire.BytesType)
	envelopeMsg = protowire.AppendString(envelopeMsg, "f62f5f8c-3cce-4a70-bce3-d6361ad95010")
	envelopeMsg = protowire.AppendTag(envelopeMsg, fieldBodySessionEnd, protowire.BytesType)
	envelopeMsg = protowire.AppendBytes(envelopeMsg, nil)

	envelope, err := UnmarshalOrchestratorEnvelope(envelopeMsg)
	if err != nil {
		t.Fatalf("UnmarshalOrchestratorEnvelope() error = %v", err)
	}
	if !envelope.SessionEnd {
		t.Fatalf("expected session_end=true")
	}
}
