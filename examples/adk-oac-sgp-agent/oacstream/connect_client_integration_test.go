package oacstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestConnectClientRunRoundTrip(t *testing.T) {
	t.Parallel()

	type observedResponse struct {
		sessionID string
		success   bool
		errMsg    string
	}

	responses := make(chan observedResponse, 2)

	srv := httptest.NewUnstartedServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Content-Type"); got != "application/connect+proto" {
				t.Errorf("content-type = %q, want application/connect+proto", got)
			}
			if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
				t.Errorf("connect protocol version = %q, want 1", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Errorf("authorization = %q, want Bearer test-token", got)
			}

			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("response writer is not a flusher")
			}

			eventBytes := marshalOrchestratorEventEnvelope(
				"5e932639-6265-4f22-bebd-5eb6b16cb25f",
				"agent.events",
				[]byte("hello"),
				"text/plain",
			)
			if _, err := w.Write(frameConnectData(eventBytes)); err != nil {
				t.Fatalf("write event frame: %v", err)
			}
			flusher.Flush()

			sessionEndBytes := marshalOrchestratorSessionEndEnvelope(
				"5e932639-6265-4f22-bebd-5eb6b16cb25f",
			)
			if _, err := w.Write(frameConnectData(sessionEndBytes)); err != nil {
				t.Fatalf("write session_end frame: %v", err)
			}
			flusher.Flush()

			reader := bufio.NewReader(r.Body)
			for i := 0; i < 2; i++ {
				payload, err := readEnvelopeFrame(reader)
				if err != nil {
					t.Fatalf("read harness frame %d: %v", i, err)
				}
				sessionID, success, errMsg, err := unmarshalHarnessEnvelopeForTest(payload)
				if err != nil {
					t.Fatalf("decode harness frame %d: %v", i, err)
				}
				responses <- observedResponse{sessionID: sessionID, success: success, errMsg: errMsg}
			}
		}),
	)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	client, err := NewConnectClientWithHTTPClient(srv.URL, "test-token", srv.Client())
	if err != nil {
		t.Fatalf("NewConnectClientWithHTTPClient() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var handledMu sync.Mutex
	handledCount := 0

	err = client.Run(
		ctx,
		func(_ context.Context, incoming OrchestratorEnvelope) (HarnessEnvelope, error) {
			handledMu.Lock()
			handledCount++
			handledMu.Unlock()

			return HarnessEnvelope{
				SessionID: incoming.SessionID,
				Result:    &EventResult{Success: true},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	handledMu.Lock()
	count := handledCount
	handledMu.Unlock()
	if count != 2 {
		t.Fatalf("handled envelopes = %d, want 2", count)
	}

	for i := 0; i < 2; i++ {
		select {
		case response := <-responses:
			if response.sessionID != "5e932639-6265-4f22-bebd-5eb6b16cb25f" {
				t.Fatalf("response session_id = %s, want expected session", response.sessionID)
			}
			if !response.success {
				t.Fatalf("response success = false, err = %q", response.errMsg)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for harness response %d", i)
		}
	}
}

func frameConnectData(payload []byte) []byte {
	frame := make([]byte, envelopeHeaderLen+len(payload))
	frame[0] = flagData
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)
	return frame
}

func marshalOrchestratorEventEnvelope(
	sessionID, channel string,
	payload []byte,
	contentType string,
) []byte {
	eventMsg := make([]byte, 0, 64)
	eventMsg = protowire.AppendTag(eventMsg, fieldEventChannel, protowire.BytesType)
	eventMsg = protowire.AppendString(eventMsg, channel)
	eventMsg = protowire.AppendTag(eventMsg, fieldEventPayload, protowire.BytesType)
	eventMsg = protowire.AppendBytes(eventMsg, payload)
	eventMsg = protowire.AppendTag(eventMsg, fieldEventContentType, protowire.BytesType)
	eventMsg = protowire.AppendString(eventMsg, contentType)

	envelope := make([]byte, 0, 96)
	envelope = protowire.AppendTag(envelope, fieldSessionID, protowire.BytesType)
	envelope = protowire.AppendString(envelope, sessionID)
	envelope = protowire.AppendTag(envelope, fieldBodyEvent, protowire.BytesType)
	envelope = protowire.AppendBytes(envelope, eventMsg)
	return envelope
}

func marshalOrchestratorSessionEndEnvelope(sessionID string) []byte {
	envelope := make([]byte, 0, 48)
	envelope = protowire.AppendTag(envelope, fieldSessionID, protowire.BytesType)
	envelope = protowire.AppendString(envelope, sessionID)
	envelope = protowire.AppendTag(envelope, fieldBodySessionEnd, protowire.BytesType)
	envelope = protowire.AppendBytes(envelope, nil)
	return envelope
}

func unmarshalHarnessEnvelopeForTest(data []byte) (string, bool, string, error) {
	var (
		sessionID string
		success   bool
		errMsg    string
	)

	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", false, "", protowire.ParseError(n)
		}
		data = data[n:]

		switch number {
		case fieldSessionID:
			if typ != protowire.BytesType {
				return "", false, "", io.ErrUnexpectedEOF
			}
			value, m := protowire.ConsumeString(data)
			if m < 0 {
				return "", false, "", protowire.ParseError(m)
			}
			sessionID = value
			data = data[m:]
		case fieldBody:
			if typ != protowire.BytesType {
				return "", false, "", io.ErrUnexpectedEOF
			}
			bodyBytes, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return "", false, "", protowire.ParseError(m)
			}
			data = data[m:]

			br := bytes.Clone(bodyBytes)
			for len(br) > 0 {
				innerNum, innerType, innerN := protowire.ConsumeTag(br)
				if innerN < 0 {
					return "", false, "", protowire.ParseError(innerN)
				}
				br = br[innerN:]
				switch innerNum {
				case fieldResultSuccess:
					if innerType != protowire.VarintType {
						return "", false, "", io.ErrUnexpectedEOF
					}
					v, k := protowire.ConsumeVarint(br)
					if k < 0 {
						return "", false, "", protowire.ParseError(k)
					}
					success = v == 1
					br = br[k:]
				case fieldResultError:
					if innerType != protowire.BytesType {
						return "", false, "", io.ErrUnexpectedEOF
					}
					value, k := protowire.ConsumeString(br)
					if k < 0 {
						return "", false, "", protowire.ParseError(k)
					}
					errMsg = value
					br = br[k:]
				default:
					k := protowire.ConsumeFieldValue(innerNum, innerType, br)
					if k < 0 {
						return "", false, "", protowire.ParseError(k)
					}
					br = br[k:]
				}
			}
		default:
			k := protowire.ConsumeFieldValue(number, typ, data)
			if k < 0 {
				return "", false, "", protowire.ParseError(k)
			}
			data = data[k:]
		}
	}

	return sessionID, success, errMsg, nil
}
