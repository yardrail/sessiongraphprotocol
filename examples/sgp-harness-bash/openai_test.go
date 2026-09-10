package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletionRoundTrip(t *testing.T) {
	var gotReq chatRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		resp := chatResponse{Choices: []chatChoice{{Message: chatMessage{
			Role: "assistant",
			ToolCalls: []chatToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: chatFunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"foo.txt"}`,
				},
			}},
		}}}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	resp, err := chatCompletion(
		context.Background(),
		server.URL,
		"test-key",
		"gpt-4o-mini",
		[]chatMessage{{Role: "user", Content: "read foo.txt"}},
		toolDefinitions(),
	)
	if err != nil {
		t.Fatalf("chatCompletion: %v", err)
	}

	if gotReq.Model != "gpt-4o-mini" {
		t.Fatalf("unexpected model sent: %s", gotReq.Model)
	}
	if len(gotReq.Tools) != 2 {
		t.Fatalf("expected 2 tools sent, got %d", len(gotReq.Tools))
	}

	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"foo.txt"}` {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
}
