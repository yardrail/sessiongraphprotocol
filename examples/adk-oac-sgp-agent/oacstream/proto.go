package oacstream

import (
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	fieldSessionID = 1
	fieldBody      = 2
)

const (
	fieldBodyEvent      = 2
	fieldBodySessionEnd = 3
)

const (
	fieldResultSuccess = 1
	fieldResultError   = 2
)

const (
	fieldEventChannel     = 1
	fieldEventPayload     = 2
	fieldEventContentType = 3
)

// HarnessEnvelope is sent from harness to orchestrator.
type HarnessEnvelope struct {
	SessionID string
	Result    *EventResult
}

// EventResult indicates whether a delivered event was processed.
type EventResult struct {
	Success      bool
	ErrorMessage string
}

// OrchestratorEnvelope is sent from orchestrator to harness.
type OrchestratorEnvelope struct {
	SessionID  string
	Event      *Event
	SessionEnd bool
}

// Event is the OAC-delivered event payload.
type Event struct {
	Channel     string
	Payload     []byte
	ContentType string
}

func MarshalHarnessEnvelope(envelope HarnessEnvelope) ([]byte, error) {
	if envelope.SessionID == "" {
		return nil, errors.New("session_id is required")
	}
	if envelope.Result == nil {
		return nil, errors.New("result is required")
	}

	body := make([]byte, 0, 64)
	body = protowire.AppendTag(body, fieldResultSuccess, protowire.VarintType)
	if envelope.Result.Success {
		body = protowire.AppendVarint(body, 1)
	} else {
		body = protowire.AppendVarint(body, 0)
	}
	if envelope.Result.ErrorMessage != "" {
		body = protowire.AppendTag(body, fieldResultError, protowire.BytesType)
		body = protowire.AppendString(body, envelope.Result.ErrorMessage)
	}

	msg := make([]byte, 0, len(body)+48)
	msg = protowire.AppendTag(msg, fieldSessionID, protowire.BytesType)
	msg = protowire.AppendString(msg, envelope.SessionID)
	msg = protowire.AppendTag(msg, fieldBody, protowire.BytesType)
	msg = protowire.AppendBytes(msg, body)

	return msg, nil
}

func UnmarshalOrchestratorEnvelope(data []byte) (OrchestratorEnvelope, error) {
	var envelope OrchestratorEnvelope

	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return OrchestratorEnvelope{}, fmt.Errorf("consume tag: %v", protowire.ParseError(n))
		}
		data = data[n:]

		switch number {
		case fieldSessionID:
			if typ != protowire.BytesType {
				return OrchestratorEnvelope{}, errors.New("session_id has invalid wire type")
			}
			value, m := protowire.ConsumeString(data)
			if m < 0 {
				return OrchestratorEnvelope{}, fmt.Errorf("consume session_id: %v", protowire.ParseError(m))
			}
			envelope.SessionID = value
			data = data[m:]
		case fieldBodyEvent:
			if typ != protowire.BytesType {
				return OrchestratorEnvelope{}, errors.New("event has invalid wire type")
			}
			bytesValue, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return OrchestratorEnvelope{}, fmt.Errorf("consume event: %v", protowire.ParseError(m))
			}
			event, err := unmarshalEvent(bytesValue)
			if err != nil {
				return OrchestratorEnvelope{}, err
			}
			envelope.Event = &event
			data = data[m:]
		case fieldBodySessionEnd:
			if typ != protowire.BytesType {
				return OrchestratorEnvelope{}, errors.New("session_end has invalid wire type")
			}
			_, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return OrchestratorEnvelope{}, fmt.Errorf("consume session_end: %v", protowire.ParseError(m))
			}
			envelope.SessionEnd = true
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(number, typ, data)
			if m < 0 {
				return OrchestratorEnvelope{}, fmt.Errorf("skip unknown field %d: %v", number, protowire.ParseError(m))
			}
			data = data[m:]
		}
	}

	if envelope.SessionID == "" {
		return OrchestratorEnvelope{}, errors.New("session_id is required")
	}
	if envelope.Event == nil && !envelope.SessionEnd {
		return OrchestratorEnvelope{}, errors.New("orchestrator envelope has no body")
	}
	if envelope.Event != nil && envelope.SessionEnd {
		return OrchestratorEnvelope{}, errors.New("orchestrator envelope has multiple bodies")
	}

	return envelope, nil
}

func unmarshalEvent(data []byte) (Event, error) {
	var event Event
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return Event{}, fmt.Errorf("consume event tag: %v", protowire.ParseError(n))
		}
		data = data[n:]

		switch number {
		case fieldEventChannel:
			if typ != protowire.BytesType {
				return Event{}, errors.New("event.channel has invalid wire type")
			}
			value, m := protowire.ConsumeString(data)
			if m < 0 {
				return Event{}, fmt.Errorf("consume event.channel: %v", protowire.ParseError(m))
			}
			event.Channel = value
			data = data[m:]
		case fieldEventPayload:
			if typ != protowire.BytesType {
				return Event{}, errors.New("event.payload has invalid wire type")
			}
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return Event{}, fmt.Errorf("consume event.payload: %v", protowire.ParseError(m))
			}
			event.Payload = append([]byte(nil), value...)
			data = data[m:]
		case fieldEventContentType:
			if typ != protowire.BytesType {
				return Event{}, errors.New("event.content_type has invalid wire type")
			}
			value, m := protowire.ConsumeString(data)
			if m < 0 {
				return Event{}, fmt.Errorf("consume event.content_type: %v", protowire.ParseError(m))
			}
			event.ContentType = value
			data = data[m:]
		default:
			m := protowire.ConsumeFieldValue(number, typ, data)
			if m < 0 {
				return Event{}, fmt.Errorf("skip unknown event field %d: %v", number, protowire.ParseError(m))
			}
			data = data[m:]
		}
	}

	if event.Channel == "" {
		return Event{}, errors.New("event.channel is required")
	}
	return event, nil
}
