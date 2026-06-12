package liveagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	sgp "github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
)

type Harness struct {
	workspaceRoot string
	store         sgp.Store
	generator     ModelGenerator
	model         string
	systemPrompt  string

	mu       sync.Mutex
	consoles map[sgp.ID]*Console
}

func NewHarness(
	workspaceRoot string,
	store sgp.Store,
	generator ModelGenerator,
	model string,
	systemPrompt string,
) (*Harness, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, errors.New("workspace root is required")
	}
	if generator == nil {
		return nil, errors.New("model generator is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("model name is required")
	}

	return &Harness{
		workspaceRoot: workspaceRoot,
		store:         store,
		generator:     generator,
		model:         model,
		systemPrompt:  systemPrompt,
		consoles:      make(map[sgp.ID]*Console),
	}, nil
}

func (harness *Harness) HandleEvent(
	ctx context.Context,
	sessionID, channel, contentType string,
	payload []byte,
) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", errors.New("session id is required")
	}

	input, err := decodeHarnessInput(payload, contentType)
	if err != nil {
		return "", fmt.Errorf("decode %s event: %w", channel, err)
	}

	console, err := harness.consoleForSession(ctx, sgp.ID(sessionID))
	if err != nil {
		return "", err
	}

	return console.HandleUserTurn(ctx, harness.generator, harness.model, input)
}

func (harness *Harness) EndSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return errors.New("session id is required")
	}

	harness.mu.Lock()
	console := harness.consoles[sgp.ID(sessionID)]
	delete(harness.consoles, sgp.ID(sessionID))
	harness.mu.Unlock()

	if console == nil {
		return nil
	}

	return console.Close(ctx)
}

func (harness *Harness) consoleForSession(ctx context.Context, sessionID sgp.ID) (*Console, error) {
	harness.mu.Lock()
	defer harness.mu.Unlock()

	if console := harness.consoles[sessionID]; console != nil {
		return console, nil
	}

	console, err := NewConsoleForSession(
		harness.workspaceRoot,
		harness.store,
		harness.systemPrompt,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("create session console: %w", err)
	}

	harness.consoles[sessionID] = console
	return console, nil
}

func decodeHarnessInput(payload []byte, contentType string) (string, error) {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	text := strings.TrimSpace(string(payload))
	if contentType == "" || strings.HasPrefix(contentType, "text/plain") {
		if text == "" {
			return "", errors.New("payload is empty")
		}
		return text, nil
	}

	if strings.HasPrefix(contentType, "application/json") {
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			return "", fmt.Errorf("unmarshal json payload: %w", err)
		}

		for _, key := range []string{"input", "content", "text", "message"} {
			value, ok := body[key].(string)
			if ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
			}
		}

		return "", errors.New("json payload must include one of input, content, text, or message")
	}

	if text == "" {
		return "", fmt.Errorf("unsupported content type %q with empty payload", contentType)
	}

	return text, nil
}
