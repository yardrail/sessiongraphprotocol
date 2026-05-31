package liveagent

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestPromptBeforePullDeclinesDownload(t *testing.T) {
	t.Parallel()

	var pullCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			pullCalled = true
			response.WriteHeader(http.StatusOK)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL)
	input := bufio.NewReader(strings.NewReader("n\n"))
	var output bytes.Buffer

	err := PromptBeforePull(context.Background(), input, &output, client, defaultOllamaModel)
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("PromptBeforePull() error = %v, want declined error", err)
	}
	if pullCalled {
		t.Fatal("pull endpoint was called after decline")
	}
}

func TestPromptBeforePullPullsWithConsent(t *testing.T) {
	t.Parallel()

	var pullCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/tags":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			pullCalled = true
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte("{\"status\":\"pulling\"}\n{\"status\":\"success\"}\n"))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewOllamaClient(server.URL)
	input := bufio.NewReader(strings.NewReader("y\n"))
	var output bytes.Buffer

	err := PromptBeforePull(context.Background(), input, &output, client, defaultOllamaModel)
	if err != nil {
		t.Fatalf("PromptBeforePull() error = %v", err)
	}
	if !pullCalled {
		t.Fatal("pull endpoint was not called after consent")
	}
	if !strings.Contains(output.String(), "pulling") || !strings.Contains(output.String(), "success") {
		t.Fatalf("pull output = %q, want status lines", output.String())
	}
}

func TestOllamaGeneratorParsesToolCalls(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/chat":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"message":{"role":"assistant","content":"{\"type\":\"tool_calls\",\"tool_calls\":[{\"name\":\"list_files\",\"args\":{\"limit\":2}}]}"},"done":true}`))
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	generator := NewOllamaGenerator(server.URL, defaultOllamaModel)
	response, err := generator.GenerateContent(context.Background(), defaultOllamaModel, nil, &genai.GenerateContentConfig{})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}

	calls := response.FunctionCalls()
	if len(calls) != 1 {
		t.Fatalf("len(FunctionCalls()) = %d, want 1", len(calls))
	}
	if calls[0].Name != "list_files" {
		t.Fatalf("FunctionCall.Name = %s, want list_files", calls[0].Name)
	}
}
