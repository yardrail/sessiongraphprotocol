package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"

	"golang.org/x/net/http2"

	oacv1 "github.com/restrukt-ai/openagentcontainers/pkg/api/oac/v1"
	"github.com/restrukt-ai/openagentcontainers/pkg/api/oac/v1/oacv1connect"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

// oacConfig holds OAC runtime configuration read from environment variables.
type oacConfig struct {
	orchestratorAddr string // ORCHESTRATOR_ADDR
	authToken        string // HARNESS_AUTH_TOKEN
	sessionID        string // SESSION_ID
	inferenceBaseURL string // INFERENCE_BASE_URL
	model            string // MODEL
}

// loadOACConfig reads OAC env vars. Returns nil when ORCHESTRATOR_ADDR is unset
// (standalone mode).
func loadOACConfig() *oacConfig {
	addr := os.Getenv("ORCHESTRATOR_ADDR")
	if addr == "" {
		return nil
	}

	model := os.Getenv("MODEL")
	if model == "" {
		model = "llama3.2"
	}

	baseURL := os.Getenv("INFERENCE_BASE_URL")
	if baseURL == "" {
		// Fall back to orchestrator addr + /v1/inference if not explicitly set.
		baseURL = addr + "/v1/inference"
	}

	return &oacConfig{
		orchestratorAddr: addr,
		authToken:        os.Getenv("HARNESS_AUTH_TOKEN"),
		sessionID:        os.Getenv("SESSION_ID"),
		inferenceBaseURL: baseURL,
		model:            model,
	}
}

// runOACMode opens a RunSession bidi stream to the orchestrator and processes
// incoming Event frames until the stream closes or ctx is cancelled.
func runOACMode(ctx context.Context, cfg *oacConfig) error {
	log := slog.Default()

	// h2c transport: HTTP/2 cleartext (no TLS) as required by the orchestrator.
	h2cTransport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}

	var rt http.RoundTripper = h2cTransport
	if cfg.authToken != "" {
		rt = &oacBearerTransport{base: h2cTransport, token: cfg.authToken}
	}

	c := oacv1connect.NewHarnessServiceClient(
		&http.Client{Transport: rt},
		cfg.orchestratorAddr,
	)

	stream := c.RunSession(ctx)
	defer stream.CloseResponse()

	// Send an initial heartbeat to open the HTTP/2 stream toward the server.
	// ConnectRPC bidi streams are not initiated until the first Send(), so the
	// server handler would never be invoked if we only called Receive().
	if err := stream.Send(&oacv1.RunSessionRequest{
		Frame: &oacv1.RunSessionRequest_Heartbeat{Heartbeat: &oacv1.Heartbeat{}},
	}); err != nil {
		return fmt.Errorf("oac: initial heartbeat: %w", err)
	}

	log.Info("oac: connected", "orchestrator", cfg.orchestratorAddr, "session", cfg.sessionID)

	// history holds the full conversation, rebuilt from SessionContext on connect.
	var history []ollamaMessage

	for {
		msg, err := stream.Receive()
		if err == io.EOF {
			log.Info("oac: stream closed")
			return nil
		}

		if err != nil {
			return fmt.Errorf("oac: receive: %w", err)
		}

		switch f := msg.GetFrame().(type) {
		case *oacv1.RunSessionResponse_SessionContext:
			history = historyFromContext(f.SessionContext)
			log.Info("oac: session context", "messages", len(history))

		case *oacv1.RunSessionResponse_Event:
			evt := f.Event
			userText := string(evt.GetPayload())
			log.Info("oac: event", "channel", evt.GetChannelName(), "bytes", len(userText))

			history = append(history, ollamaMessage{Role: "user", Content: userText})
			history = runInferenceTurns(ctx, cfg, history)

			sendErr := stream.Send(&oacv1.RunSessionRequest{
				Frame: &oacv1.RunSessionRequest_Idle{Idle: &oacv1.Idle{}},
			})
			if sendErr != nil {
				return fmt.Errorf("oac: send idle: %w", sendErr)
			}

			// Orchestrator closes the stream after receiving Idle.
			log.Info("oac: idle sent, stream closing")
			return nil

		case *oacv1.RunSessionResponse_SuspendWarning:
			log.Info("oac: suspend warning")
		}
	}
}

// runInferenceTurns runs the tool-call loop and returns the updated history.
func runInferenceTurns(
	ctx context.Context,
	cfg *oacConfig,
	history []ollamaMessage,
) []ollamaMessage {
	log := slog.Default()

	for range maxToolSteps {
		resp, err := ollamaChat(
			ctx,
			cfg.inferenceBaseURL,
			cfg.model,
			cfg.sessionID,
			history,
			toolDefinitions(),
		)
		if err != nil {
			log.Error("oac: inference error", "err", err)
			return history
		}

		if len(resp.Message.ToolCalls) == 0 {
			log.Info("oac: inference result", "content", resp.Message.Content)
			history = append(
				history,
				ollamaMessage{Role: "assistant", Content: resp.Message.Content},
			)
			return history
		}

		// Append assistant tool-call message.
		tcs := make([]ollamaToolCall, len(resp.Message.ToolCalls))
		for i, tc := range resp.Message.ToolCalls {
			tcs[i] = ollamaToolCall{Function: ollamaFunction{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}}
		}

		history = append(history, ollamaMessage{Role: "assistant", ToolCalls: tcs})

		// Execute each tool and append results.
		for _, tc := range resp.Message.ToolCalls {
			argsBytes, _ := json.Marshal(tc.Function.Arguments)
			output, _ := executeTool(ctx, tc.Function.Name, string(argsBytes))
			history = append(history, ollamaMessage{Role: "tool", Content: output})
		}
	}

	return history
}

// historyFromContext converts a SessionContext's HistoryJson into ollama messages.
func historyFromContext(sc *oacv1.SessionContext) []ollamaMessage {
	if len(sc.GetHistoryJson()) == 0 {
		return nil
	}

	var msgs []sgp.Message
	if err := json.Unmarshal(sc.GetHistoryJson(), &msgs); err != nil {
		slog.Default().Warn("oac: parse history", "err", err)
		return nil
	}

	result := make([]ollamaMessage, 0, len(msgs))

	for _, m := range msgs {
		switch {
		case m.System != nil:
			result = append(result, ollamaMessage{Role: "system", Content: m.System.Text})
		case m.User != nil:
			result = append(result, ollamaMessage{Role: "user", Content: m.TextContent()})
		case m.Assistant != nil && len(m.Assistant.ToolCalls) > 0:
			tcs := make([]ollamaToolCall, len(m.Assistant.ToolCalls))
			for i, tc := range m.Assistant.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(tc.Arguments), &args)
				tcs[i] = ollamaToolCall{Function: ollamaFunction{Name: tc.Name, Arguments: args}}
			}
			result = append(result, ollamaMessage{Role: "assistant", ToolCalls: tcs})
		case m.Assistant != nil:
			result = append(result, ollamaMessage{Role: "assistant", Content: m.TextContent()})
		case m.Tool != nil:
			result = append(result, ollamaMessage{Role: "tool", Content: m.TextContent()})
		}
	}

	return result
}

// oacBearerTransport adds an Authorization: Bearer header to every request.
type oacBearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t *oacBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+t.token)

	return t.base.RoundTrip(clone)
}
