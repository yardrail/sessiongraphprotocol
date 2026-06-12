package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"

	sgp "github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	jsonstore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/json"
)

const maxToolSteps = 10

var errTeleported = errors.New("teleported")

type harness struct {
	graph      *sgp.Graph
	store      *jsonstore.JSONFileStore
	headID     sgp.ID
	ollamaURL  string
	model      string
	sessionDir string
	toolsDesc  string
	peersDesc  string
	callSeq    int
}

func newHarness(
	sessionDir, sessionID, ollamaURL, model, systemPrompt, toolsDesc, peersDesc string,
) (*harness, sgp.ID, error) {
	store, err := jsonstore.NewJSONFileStore(sessionDir)
	if err != nil {
		return nil, "", fmt.Errorf("create store: %w", err)
	}

	var graph *sgp.Graph
	var headID sgp.ID

	if sessionID == "" {
		graph = sgp.NewGraph()
		root, _, err := graph.Append(sgp.Message{System: &sgp.SystemMessage{Text: systemPrompt}})
		if err != nil {
			return nil, "", fmt.Errorf("append system message: %w", err)
		}
		headID = root.ID
		if err := store.Save(context.Background(), graph); err != nil {
			return nil, "", fmt.Errorf("save initial graph: %w", err)
		}
	} else {
		graph, err = store.Load(context.Background(), sgp.ID(sessionID))
		if err != nil {
			return nil, "", fmt.Errorf("load session: %w", err)
		}
		if head, ok := graph.Head(); ok {
			headID = head.ID
		}
	}

	return &harness{
		graph:      graph,
		store:      store,
		headID:     headID,
		ollamaURL:  ollamaURL,
		model:      model,
		sessionDir: sessionDir,
		toolsDesc:  toolsDesc,
		peersDesc:  peersDesc,
	}, graph.Session().ID, nil
}

func (h *harness) handleTurn(ctx context.Context, userInput string) (string, error) {
	userNode, _, err := h.graph.Append(
		sgp.Message{
			User: &sgp.UserMessage{
				Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: userInput}}},
			},
		},
		h.headID,
	)
	if err != nil {
		return "", fmt.Errorf("append user message: %w", err)
	}
	h.headID = userNode.ID
	if err := h.persist(ctx); err != nil {
		return "", err
	}

	return h.runInferenceLoop(ctx)
}

func (h *harness) runInferenceLoop(ctx context.Context) (string, error) {
	for range maxToolSteps {
		nodes, err := h.graph.ResumeNodes(h.headID)
		if err != nil {
			return "", fmt.Errorf("resume nodes: %w", err)
		}

		resp, err := ollamaChat(
			ctx,
			h.ollamaURL,
			h.model,
			toOllamaMessages(nodes),
			toolDefinitions(),
		)
		if err != nil {
			return "", fmt.Errorf("ollama chat: %w", err)
		}

		if len(resp.Message.ToolCalls) == 0 {
			text := resp.Message.Content
			assistNode, _, err := h.graph.Append(
				sgp.Message{Assistant: &sgp.AssistantMessage{
					Parts: []sgp.ContentPart{{Text: &sgp.TextPart{Text: text}}},
				}},
				h.headID,
			)
			if err != nil {
				return "", fmt.Errorf("append assistant message: %w", err)
			}
			h.headID = assistNode.ID
			if err := h.persist(ctx); err != nil {
				return "", err
			}
			return text, nil
		}

		h.callSeq++
		toolCalls := make([]sgp.ToolCall, len(resp.Message.ToolCalls))
		for i, tc := range resp.Message.ToolCalls {
			argsBytes, _ := json.Marshal(tc.Function.Arguments)
			toolCalls[i] = sgp.ToolCall{
				ID:        fmt.Sprintf("tc-%d-%d", h.callSeq, i),
				Name:      tc.Function.Name,
				Arguments: string(argsBytes),
			}
		}

		callNode, _, err := h.graph.Append(
			sgp.Message{Assistant: &sgp.AssistantMessage{ToolCalls: toolCalls}},
			h.headID,
		)
		if err != nil {
			return "", fmt.Errorf("append tool call node: %w", err)
		}
		h.headID = callNode.ID

		// Intercept teleport before executing any tools.
		teleportIdx := -1
		for i, tc := range resp.Message.ToolCalls {
			if tc.Function.Name == "teleport" {
				teleportIdx = i
				break
			}
		}

		if teleportIdx >= 0 {
			if err := h.persist(ctx); err != nil {
				return "", err
			}
			tc := resp.Message.ToolCalls[teleportIdx]
			argsBytes, _ := json.Marshal(tc.Function.Arguments)
			var args map[string]any
			_ = json.Unmarshal(argsBytes, &args)
			spawnErr := h.spawnHandoff(args)
			if spawnErr != nil {
				resultNode, _, appendErr := h.graph.Append(
					sgp.Message{Tool: &sgp.ToolMessage{
						ToolCallID: fmt.Sprintf("tc-%d-%d", h.callSeq, teleportIdx),
						Name:       "teleport",
						Parts: []sgp.ContentPart{
							{Text: &sgp.TextPart{Text: spawnErr.Error()}},
						},
						IsError: true,
					}},
					h.headID,
				)
				if appendErr != nil {
					return "", fmt.Errorf("append teleport error: %w", appendErr)
				}
				h.headID = resultNode.ID
				if err := h.persist(ctx); err != nil {
					return "", err
				}
				continue
			}
			return "", errTeleported
		}

		for i, tc := range resp.Message.ToolCalls {
			argsBytes, _ := json.Marshal(tc.Function.Arguments)
			output, success := executeTool(ctx, tc.Function.Name, string(argsBytes))
			resultNode, _, err := h.graph.Append(
				sgp.Message{Tool: &sgp.ToolMessage{
					ToolCallID: fmt.Sprintf("tc-%d-%d", h.callSeq, i),
					Name:       tc.Function.Name,
					Parts:      []sgp.ContentPart{{Text: &sgp.TextPart{Text: output}}},
					IsError:    !success,
				}},
				h.headID,
			)
			if err != nil {
				return "", fmt.Errorf("append tool result: %w", err)
			}
			h.headID = resultNode.ID
		}

		if err := h.persist(ctx); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("exceeded %d tool steps without final response", maxToolSteps)
}

func (h *harness) spawnHandoff(args map[string]any) error {
	dest, _ := args["destination"].(string)
	if dest == "" {
		return fmt.Errorf("teleport: destination is required")
	}
	cmd := exec.Command(dest,
		"--session-id", string(h.graph.Session().ID),
		"--model", h.model,
		"--ollama-url", h.ollamaURL,
		"--session-dir", h.sessionDir,
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

func (h *harness) handleArrival(ctx context.Context, selfPath string) (string, bool, error) {
	head, ok := h.graph.Head()
	if !ok {
		return "", false, nil
	}
	if head.Message.Assistant == nil || len(head.Message.Assistant.ToolCalls) == 0 {
		return "", false, nil
	}

	// Head is an AssistantMessage with unanswered ToolCalls — arrival/crash-recovery case.
	for _, tc := range head.Message.Assistant.ToolCalls {
		var text string
		var isError bool
		if tc.Name == "teleport" {
			text = fmt.Sprintf("Arrived at %s. %s", selfPath, h.toolsDesc)
			if h.peersDesc != "" {
				text += "\n" + h.peersDesc
			}
		} else {
			output, success := executeTool(ctx, tc.Name, tc.Arguments)
			text = output
			isError = !success
		}
		resultNode, _, err := h.graph.Append(
			sgp.Message{Tool: &sgp.ToolMessage{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Parts:      []sgp.ContentPart{{Text: &sgp.TextPart{Text: text}}},
				IsError:    isError,
			}},
			h.headID,
		)
		if err != nil {
			return "", false, fmt.Errorf("append arrival tool result: %w", err)
		}
		h.headID = resultNode.ID
	}

	if err := h.persist(ctx); err != nil {
		return "", false, err
	}

	response, err := h.runInferenceLoop(ctx)
	if err != nil {
		return "", true, err
	}
	return response, true, nil
}

func (h *harness) persist(ctx context.Context) error {
	return h.store.Save(ctx, h.graph)
}

func (h *harness) close(ctx context.Context) {
	_, _ = h.graph.End()
	_ = h.persist(ctx)
}

func toOllamaMessages(nodes []sgp.Node) []ollamaMessage {
	msgs := make([]ollamaMessage, 0, len(nodes))
	for _, node := range nodes {
		if len(node.SynthesizedFrom) > 0 {
			continue
		}
		m := node.Message
		switch {
		case m.System != nil:
			msgs = append(msgs, ollamaMessage{Role: "system", Content: m.System.Text})
		case m.User != nil:
			msgs = append(msgs, ollamaMessage{Role: "user", Content: m.TextContent()})
		case m.Assistant != nil && len(m.Assistant.ToolCalls) > 0:
			tcs := make([]ollamaToolCall, len(m.Assistant.ToolCalls))
			for i, tc := range m.Assistant.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(tc.Arguments), &args)
				tcs[i] = ollamaToolCall{Function: ollamaFunction{Name: tc.Name, Arguments: args}}
			}
			msgs = append(msgs, ollamaMessage{Role: "assistant", ToolCalls: tcs})
		case m.Assistant != nil:
			msgs = append(msgs, ollamaMessage{Role: "assistant", Content: m.TextContent()})
		case m.Tool != nil:
			msgs = append(msgs, ollamaMessage{Role: "tool", Content: m.TextContent()})
		}
	}
	return msgs
}
