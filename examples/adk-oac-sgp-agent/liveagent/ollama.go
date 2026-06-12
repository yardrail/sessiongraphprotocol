package liveagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"
)

const (
	defaultOllamaBaseURL = "http://localhost:11434"
	defaultOllamaModel   = "qwen2.5-coder:3b"
)

const ollamaActionPrompt = `You are a repo-local coding agent.

Respond with strict JSON only. Do not use markdown fences or extra prose.

Use this schema:
{"type":"final","content":"your answer"}
or
{"type":"tool_calls","tool_calls":[{"name":"list_files","args":{...}},{"name":"read_file","args":{...}}]}

Available tools:
- list_files: args may include path_prefix and limit
- read_file: args must include path, optional start_line and end_line
- grep_text: args must include query, optional path_prefix and limit
- spawn_subagent_search: args may include question, query, path_prefix, limit

If multiple independent tool calls are useful, emit them together in one tool_calls array.
If you need to summarize after tools are used, emit a final response.`

type OllamaClient struct {
	baseURL    string
	httpClient *http.Client
}

type OllamaGenerator struct {
	client *OllamaClient
	model  string
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   string          `json:"format,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaChatResponse struct {
	Message ollamaMessage `json:"message"`
	Done    bool          `json:"done"`
}

type ollamaTagsResponse struct {
	Models []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	} `json:"models"`
}

type ollamaPullRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

type ollamaActionEnvelope struct {
	Type      string                 `json:"type"`
	Content   string                 `json:"content,omitempty"`
	ToolCalls []ollamaActionToolCall `json:"tool_calls,omitempty"`
}

type ollamaActionToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

func NewOllamaClient(baseURL string) *OllamaClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}

	return &OllamaClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func NewOllamaGenerator(baseURL, model string) *OllamaGenerator {
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultOllamaModel
	}

	return &OllamaGenerator{
		client: NewOllamaClient(baseURL),
		model:  model,
	}
}

func PromptBeforePull(
	ctx context.Context,
	input *bufio.Reader,
	out io.Writer,
	client *OllamaClient,
	model string,
) error {
	if input == nil {
		return errors.New("input reader is required")
	}
	if client == nil {
		return errors.New("ollama client is required")
	}

	exists, err := client.HasModel(ctx, model)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	fmt.Fprintf(out, "Ollama model %s is not installed locally.\n", model)
	fmt.Fprint(out, "Download it now? [y/N]: ")

	answer, err := input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}

	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return fmt.Errorf("model %s is not installed and download was declined", model)
	}

	return client.PullModel(ctx, model, out)
}

func (client *OllamaClient) HasModel(ctx context.Context, model string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/api/tags", nil)
	if err != nil {
		return false, fmt.Errorf("create tags request: %w", err)
	}

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("list ollama models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("list ollama models: unexpected status %s", resp.Status)
	}

	var tags ollamaTagsResponse
	if err = json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return false, fmt.Errorf("decode ollama model list: %w", err)
	}

	for _, entry := range tags.Models {
		if entry.Name == model || entry.Model == model {
			return true, nil
		}
	}

	return false, nil
}

func (client *OllamaClient) PullModel(ctx context.Context, model string, out io.Writer) error {
	body, err := json.Marshal(ollamaPullRequest{Name: model, Stream: true})
	if err != nil {
		return fmt.Errorf("marshal pull request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.baseURL+"/api/pull",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create pull request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull ollama model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return fmt.Errorf(
			"pull ollama model: unexpected status %s: %s",
			resp.Status,
			strings.TrimSpace(string(raw)),
		)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event map[string]any
		if err = json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if status, ok := event["status"].(string); ok && status != "" {
			fmt.Fprintln(out, status)
		}
	}
	if err = scanner.Err(); err != nil {
		return fmt.Errorf("read ollama pull stream: %w", err)
	}

	return nil
}

func (generator *OllamaGenerator) GenerateContent(
	ctx context.Context,
	model string,
	contents []*genai.Content,
	config *genai.GenerateContentConfig,
) (*genai.GenerateContentResponse, error) {
	if generator == nil || generator.client == nil {
		return nil, errors.New("ollama generator is not configured")
	}

	if strings.TrimSpace(model) == "" {
		model = generator.model
	}

	request := ollamaChatRequest{
		Model:    model,
		Stream:   false,
		Format:   "json",
		Messages: buildOllamaMessages(config, contents),
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama chat request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		generator.client.baseURL+"/api/chat",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("create ollama chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := generator.client.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chat with ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf(
			"chat with ollama: unexpected status %s: %s",
			resp.Status,
			strings.TrimSpace(string(raw)),
		)
	}

	var chatResponse ollamaChatResponse
	if err = json.NewDecoder(resp.Body).Decode(&chatResponse); err != nil {
		return nil, fmt.Errorf("decode ollama chat response: %w", err)
	}

	envelope, err := parseOllamaAction(strings.TrimSpace(chatResponse.Message.Content))
	if err != nil {
		return nil, err
	}

	return synthesizeGenerateContentResponse(envelope), nil
}

func buildOllamaMessages(
	config *genai.GenerateContentConfig,
	contents []*genai.Content,
) []ollamaMessage {
	messages := make([]ollamaMessage, 0, len(contents)+1)
	if config != nil && config.SystemInstruction != nil {
		messages = append(
			messages,
			ollamaMessage{
				Role:    "system",
				Content: contentToText(config.SystemInstruction) + "\n\n" + ollamaActionPrompt,
			},
		)
	} else {
		messages = append(messages, ollamaMessage{Role: "system", Content: ollamaActionPrompt})
	}

	for _, content := range contents {
		if content == nil {
			continue
		}

		role := "user"
		if content.Role == string(genai.RoleModel) {
			role = "assistant"
		}

		messages = append(messages, ollamaMessage{Role: role, Content: contentToText(content)})
	}

	return messages
}

func contentToText(content *genai.Content) string {
	if content == nil {
		return ""
	}

	parts := make([]string, 0, len(content.Parts))
	for _, part := range content.Parts {
		if part == nil {
			continue
		}

		switch {
		case part.Text != "":
			parts = append(parts, part.Text)
		case part.FunctionCall != nil:
			payload, _ := json.Marshal(part.FunctionCall)
			parts = append(parts, "FUNCTION_CALL "+string(payload))
		case part.FunctionResponse != nil:
			payload, _ := json.Marshal(part.FunctionResponse)
			parts = append(parts, "FUNCTION_RESPONSE "+string(payload))
		}
	}

	return strings.Join(parts, "\n")
}

func parseOllamaAction(raw string) (*ollamaActionEnvelope, error) {
	if raw == "" {
		return &ollamaActionEnvelope{Type: "final", Content: ""}, nil
	}

	envelope := &ollamaActionEnvelope{}
	if err := json.Unmarshal([]byte(raw), envelope); err == nil && envelope.Type != "" {
		return envelope, nil
	}

	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		trimmed := raw[start : end+1]
		if err := json.Unmarshal([]byte(trimmed), envelope); err == nil && envelope.Type != "" {
			return envelope, nil
		}
	}

	return &ollamaActionEnvelope{Type: "final", Content: raw}, nil
}

func synthesizeGenerateContentResponse(
	envelope *ollamaActionEnvelope,
) *genai.GenerateContentResponse {
	if envelope == nil {
		return &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{
				{Content: &genai.Content{Role: string(genai.RoleModel)}},
			},
		}
	}

	candidate := &genai.Candidate{Content: &genai.Content{Role: string(genai.RoleModel)}}
	switch envelope.Type {
	case "tool_calls":
		candidate.Content.Parts = make([]*genai.Part, 0, len(envelope.ToolCalls))
		for _, toolCall := range envelope.ToolCalls {
			candidate.Content.Parts = append(candidate.Content.Parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{Name: toolCall.Name, Args: toolCall.Args},
			})
		}
	default:
		candidate.Content.Parts = []*genai.Part{{Text: envelope.Content}}
	}

	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{candidate}}
}
