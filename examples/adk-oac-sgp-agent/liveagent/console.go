package liveagent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/codingagent"
	"google.golang.org/genai"
)

const (
	defaultSystemPrompt  = "You are a repo-local coding agent. Use the available tools for repository-specific questions. Prefer spawn_subagent_search for broad discovery, use read_file for exact inspection, and you may call multiple tools in the same turn when that will reduce latency."
	defaultReadLineLimit = 200
	defaultListLimit     = 50
	defaultGrepLimit     = 20
	maxToolSteps         = 8
	maxOutputChars       = 4000
)

type ModelGenerator interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

type Console struct {
	workspaceRoot string
	store         sgp.Store
	agent         *codingagent.Agent
	headID        sgp.ID
	config        *genai.GenerateContentConfig
}

type toolOutcome struct {
	call     *genai.FunctionCall
	name     string
	payload  any
	output   string
	success  bool
	nodeID   sgp.ID
	subgraph *codingagent.Agent
}

func NewConsole(workspaceRoot string, store sgp.Store, systemPrompt string) (*Console, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, errors.New("workspace root is required")
	}

	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}

	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultSystemPrompt
	}

	agent, root, err := codingagent.New(systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("create coding agent: %w", err)
	}

	console := &Console{
		workspaceRoot: absRoot,
		store:         store,
		agent:         agent,
		headID:        root.ID,
		config: &genai.GenerateContentConfig{
			Temperature:       genai.Ptr(float32(0.1)),
			MaxOutputTokens:   2048,
			SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser),
			Tools:             toolDeclarations(),
			ToolConfig: &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAuto},
			},
		},
	}

	if err = console.persist(context.Background()); err != nil {
		return nil, err
	}

	return console, nil
}

func (console *Console) SessionID() sgp.ID {
	return console.agent.Graph().Session().ID
}

func (console *Console) WorkspaceRoot() string {
	return console.workspaceRoot
}

func (console *Console) Close(ctx context.Context) error {
	if _, ok := console.agent.Graph().Head(); !ok {
		return nil
	}

	if _, err := console.agent.Graph().End(); err != nil && !errors.Is(err, sgp.ErrSessionClosed) {
		return fmt.Errorf("close graph: %w", err)
	}

	return console.persist(ctx)
}

func (console *Console) Run(ctx context.Context, in io.Reader, out io.Writer, generator ModelGenerator, model string) error {
	if generator == nil {
		return errors.New("model generator is required")
	}

	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("model name is required")
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	fmt.Fprintf(out, "coding console session: %s\n", console.SessionID())
	fmt.Fprintf(out, "workspace root: %s\n", console.workspaceRoot)
	fmt.Fprintln(out, "commands: :help, :session, :quit")

	for {
		fmt.Fprint(out, "> ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}

			return nil
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		switch line {
		case ":quit", ":exit":
			return nil
		case ":help":
			fmt.Fprintln(out, ":help shows commands, :session prints session metadata, :quit exits")
			continue
		case ":session":
			fmt.Fprintf(out, "session: %s\nworkspace: %s\n", console.SessionID(), console.workspaceRoot)
			continue
		}

		response, err := console.HandleUserTurn(ctx, generator, model, line)
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			continue
		}

		fmt.Fprintln(out, response)
	}
}

func (console *Console) HandleUserTurn(ctx context.Context, generator ModelGenerator, model string, input string) (string, error) {
	userNode, err := console.agent.AddUserTask(console.headID, input)
	if err != nil {
		return "", fmt.Errorf("append user task: %w", err)
	}

	console.headID = userNode.ID
	if err = console.persist(ctx); err != nil {
		return "", err
	}

	contents, err := console.buildPromptContents()
	if err != nil {
		return "", err
	}

	for step := 0; step < maxToolSteps; step++ {
		result, err := generator.GenerateContent(ctx, model, contents, console.config)
		if err != nil {
			return "", fmt.Errorf("generate content: %w", err)
		}

		candidateContent := firstCandidateContent(result)
		if candidateContent != nil {
			contents = append(contents, candidateContent)
		}

		calls := result.FunctionCalls()
		if len(calls) == 0 {
			response := strings.TrimSpace(result.Text())
			if response == "" {
				response = "The model returned no text."
			}

			node, _, appendErr := console.agent.Graph().Append(
				sgp.Message{Role: sgp.MessageRoleAssistant, Content: response},
				map[string]any{"kind": "assistant_response"},
				console.headID,
			)
			if appendErr != nil {
				return "", fmt.Errorf("append assistant response: %w", appendErr)
			}

			console.headID = node.ID
			if err = console.persist(ctx); err != nil {
				return "", err
			}

			return response, nil
		}

		planSummary := summarizeFunctionCalls(calls, strings.TrimSpace(result.Text()))
		planNode, err := console.agent.AddAssistantPlan(console.headID, planSummary)
		if err != nil {
			return "", fmt.Errorf("append assistant plan: %w", err)
		}

		console.headID = planNode.ID
		_, functionResponses, err := console.executeFunctionCalls(ctx, planNode.ID, calls)
		if err != nil {
			return "", err
		}

		responseParts := make([]*genai.Part, 0, len(functionResponses))
		for _, functionResponse := range functionResponses {
			responseParts = append(responseParts, &genai.Part{FunctionResponse: functionResponse})
		}

		if len(responseParts) > 0 {
			contents = append(contents, genai.NewContentFromParts(responseParts, genai.RoleUser))
		}

		if err = console.persist(ctx); err != nil {
			return "", err
		}
	}

	return "", fmt.Errorf("tool loop exceeded %d steps", maxToolSteps)
}

func (console *Console) buildPromptContents() ([]*genai.Content, error) {
	messages, err := console.agent.Graph().ResumeMessages(console.headID)
	if err != nil {
		return nil, fmt.Errorf("resume prompt messages: %w", err)
	}

	contents := make([]*genai.Content, 0, len(messages))
	for _, message := range messages {
		text := contentText(message.Content)
		if text == "" {
			continue
		}

		switch message.Role {
		case sgp.MessageRoleSystem:
			continue
		case sgp.MessageRoleUser:
			contents = append(contents, genai.NewContentFromText(text, genai.RoleUser))
		case sgp.MessageRoleAssistant:
			contents = append(contents, genai.NewContentFromText(text, genai.RoleModel))
		case sgp.MessageRoleTool:
			contents = append(contents, genai.NewContentFromText("Tool result:\n"+text, genai.RoleUser))
		}
	}

	return contents, nil
}

func (console *Console) executeFunctionCalls(ctx context.Context, planNodeID sgp.ID, calls []*genai.FunctionCall) ([]toolOutcome, []*genai.FunctionResponse, error) {
	outcomes := make([]toolOutcome, len(calls))

	var wg sync.WaitGroup
	for index, call := range calls {
		wg.Add(1)
		go func(index int, call *genai.FunctionCall) {
			defer wg.Done()
			outcomes[index] = console.executeFunctionCall(ctx, planNodeID, call)
		}(index, call)
	}
	wg.Wait()

	functionResponses := make([]*genai.FunctionResponse, 0, len(outcomes))
	branchIDs := make([]sgp.ID, 0, len(outcomes))

	for index := range outcomes {
		outcome := &outcomes[index]
		node, err := console.agent.AddToolResult(planNodeID, outcome.name, outcome.output, outcome.success)
		if err != nil {
			return nil, nil, fmt.Errorf("append tool result: %w", err)
		}

		outcome.nodeID = node.ID
		branchIDs = append(branchIDs, node.ID)

		responsePayload := map[string]any{"output": outcome.payload}
		if !outcome.success {
			responsePayload = map[string]any{"error": outcome.output}
		}

		functionResponses = append(functionResponses, &genai.FunctionResponse{
			ID:       outcome.call.ID,
			Name:     outcome.name,
			Response: responsePayload,
		})
	}

	switch {
	case len(outcomes) == 1 && !outcomes[0].success:
		summary := fmt.Sprintf("Ignored failed tool call %s and continued with the remaining context. Failure: %s", outcomes[0].name, truncate(outcomes[0].output, 240))
		rewriteNode, err := console.agent.PruneFailedToolCall(planNodeID, outcomes[0].nodeID, summary)
		if err != nil {
			return nil, nil, err
		}

		console.headID = rewriteNode.ID
	case len(outcomes) > 1:
		summary := summarizeParallelOutcomes(outcomes)
		rewriteNode, err := console.agent.SummarizeParallelToolCalls(planNodeID, summary, branchIDs...)
		if err != nil {
			return nil, nil, err
		}

		console.headID = rewriteNode.ID
	default:
		console.headID = outcomes[0].nodeID
	}

	return outcomes, functionResponses, nil
}

func (console *Console) executeFunctionCall(ctx context.Context, planNodeID sgp.ID, call *genai.FunctionCall) toolOutcome {
	if call == nil {
		return toolOutcome{name: "unknown", output: "nil function call", success: false}
	}

	switch call.Name {
	case "list_files":
		payload, output, err := console.listFiles(call.Args)
		return buildOutcome(call, payload, output, err)
	case "read_file":
		payload, output, err := console.readFile(call.Args)
		return buildOutcome(call, payload, output, err)
	case "grep_text":
		payload, output, err := console.grepText(call.Args)
		return buildOutcome(call, payload, output, err)
	case "spawn_subagent_search":
		payload, output, subgraph, err := console.spawnSubagentSearch(ctx, planNodeID, call.Args)
		outcome := buildOutcome(call, payload, output, err)
		outcome.subgraph = subgraph
		return outcome
	default:
		return buildOutcome(call, nil, "unknown tool", fmt.Errorf("unknown tool %q", call.Name))
	}
}

func (console *Console) listFiles(args map[string]any) (map[string]any, string, error) {
	pathPrefix := strings.TrimSpace(stringArg(args, "path_prefix", ""))
	limit := intArg(args, "limit", defaultListLimit)
	if limit <= 0 {
		limit = defaultListLimit
	}

	searchRoot, rootDisplay, err := console.resolveSearchRoot(pathPrefix)
	if err != nil {
		return nil, "", err
	}

	files := make([]string, 0, limit)
	err = filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		rel, err := filepath.Rel(console.workspaceRoot, path)
		if err != nil {
			return err
		}

		files = append(files, filepath.ToSlash(rel))
		if len(files) >= limit {
			return errLimitReached
		}

		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return nil, "", fmt.Errorf("walk workspace: %w", err)
	}

	sort.Strings(files)
	payload := map[string]any{"root": rootDisplay, "files": files}
	output := fmt.Sprintf("listed %d files under %s", len(files), rootDisplay)
	if len(files) > 0 {
		output += "\n" + strings.Join(files, "\n")
	}

	return payload, truncate(output, maxOutputChars), nil
}

func (console *Console) readFile(args map[string]any) (map[string]any, string, error) {
	pathValue := strings.TrimSpace(stringArg(args, "path", ""))
	if pathValue == "" {
		return nil, "", errors.New("path is required")
	}

	fullPath, displayPath, err := console.resolvePath(pathValue)
	if err != nil {
		return nil, "", err
	}

	startLine := intArg(args, "start_line", 1)
	endLine := intArg(args, "end_line", startLine+79)
	if startLine < 1 {
		return nil, "", errors.New("start_line must be >= 1")
	}
	if endLine < startLine {
		return nil, "", errors.New("end_line must be >= start_line")
	}
	if endLine-startLine+1 > defaultReadLineLimit {
		endLine = startLine + defaultReadLineLimit - 1
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	if startLine > len(lines) {
		return nil, "", fmt.Errorf("start_line %d exceeds file length %d", startLine, len(lines))
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}

	selected := lines[startLine-1 : endLine]
	text := strings.Join(selected, "\n")
	payload := map[string]any{
		"path":       displayPath,
		"start_line": startLine,
		"end_line":   endLine,
		"content":    text,
		"line_count": len(lines),
	}
	output := fmt.Sprintf("%s:%d-%d\n%s", displayPath, startLine, endLine, text)

	return payload, truncate(output, maxOutputChars), nil
}

func (console *Console) grepText(args map[string]any) (map[string]any, string, error) {
	query := strings.TrimSpace(stringArg(args, "query", ""))
	if query == "" {
		return nil, "", errors.New("query is required")
	}

	pathPrefix := strings.TrimSpace(stringArg(args, "path_prefix", ""))
	limit := intArg(args, "limit", defaultGrepLimit)
	if limit <= 0 {
		limit = defaultGrepLimit
	}

	searchRoot, rootDisplay, err := console.resolveSearchRoot(pathPrefix)
	if err != nil {
		return nil, "", err
	}

	needle := strings.ToLower(query)
	type grepMatch struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}

	matches := make([]grepMatch, 0, limit)
	err = filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if isBinary(data) {
			return nil
		}

		rel, err := filepath.Rel(console.workspaceRoot, path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(data), "\n")
		for lineIndex, line := range lines {
			if !strings.Contains(strings.ToLower(line), needle) {
				continue
			}

			matches = append(matches, grepMatch{
				Path: filepath.ToSlash(rel),
				Line: lineIndex + 1,
				Text: truncate(strings.TrimSpace(line), 240),
			})
			if len(matches) >= limit {
				return errLimitReached
			}
		}

		return nil
	})
	if err != nil && !errors.Is(err, errLimitReached) {
		return nil, "", fmt.Errorf("search workspace: %w", err)
	}

	payloadMatches := make([]map[string]any, 0, len(matches))
	linesOut := make([]string, 0, len(matches))
	for _, match := range matches {
		payloadMatches = append(payloadMatches, map[string]any{
			"path": match.Path,
			"line": match.Line,
			"text": match.Text,
		})
		linesOut = append(linesOut, fmt.Sprintf("%s:%d %s", match.Path, match.Line, match.Text))
	}

	payload := map[string]any{"root": rootDisplay, "query": query, "matches": payloadMatches}
	output := fmt.Sprintf("found %d matches for %q under %s", len(matches), query, rootDisplay)
	if len(linesOut) > 0 {
		output += "\n" + strings.Join(linesOut, "\n")
	}

	return payload, truncate(output, maxOutputChars), nil
}

func (console *Console) spawnSubagentSearch(ctx context.Context, parentNodeID sgp.ID, args map[string]any) (map[string]any, string, *codingagent.Agent, error) {
	question := strings.TrimSpace(stringArg(args, "question", ""))
	query := strings.TrimSpace(stringArg(args, "query", question))
	if question == "" {
		question = query
	}
	if query == "" {
		return nil, "", nil, errors.New("question or query is required")
	}

	pathPrefix := strings.TrimSpace(stringArg(args, "path_prefix", ""))
	limit := intArg(args, "limit", defaultGrepLimit)
	if limit <= 0 {
		limit = defaultGrepLimit
	}

	subagent, _, taskNode, err := console.agent.SpawnSubagent(parentNodeID, "You are a focused repository search subagent.", question)
	if err != nil {
		return nil, "", nil, fmt.Errorf("spawn subagent: %w", err)
	}

	planNode, err := subagent.AddAssistantPlan(taskNode.ID, fmt.Sprintf("Searching the workspace for %q", query))
	if err != nil {
		return nil, "", nil, fmt.Errorf("subagent plan: %w", err)
	}

	payload, output, err := console.grepText(map[string]any{
		"query":       query,
		"path_prefix": pathPrefix,
		"limit":       limit,
	})
	if err != nil {
		return nil, "", nil, err
	}

	finalSummary := fmt.Sprintf("Subagent summary for %q\n%s", query, output)
	_, err = subagent.AddAssistantPlan(planNode.ID, finalSummary)
	if err != nil {
		return nil, "", nil, fmt.Errorf("subagent summary: %w", err)
	}

	if _, endErr := subagent.Graph().End(); endErr != nil && !errors.Is(endErr, sgp.ErrSessionClosed) {
		return nil, "", nil, fmt.Errorf("end subagent session: %w", endErr)
	}

	if console.store != nil {
		if saveErr := console.store.Save(ctx, subagent.Graph()); saveErr != nil {
			return nil, "", nil, fmt.Errorf("persist subagent graph: %w", saveErr)
		}
	}

	spawnedFrom := subagent.Graph().Session().SpawnedFrom
	payload["subagent_session_id"] = string(subagent.Graph().Session().ID)
	if spawnedFrom != nil {
		payload["spawned_from"] = map[string]any{
			"session_id": string(spawnedFrom.SessionID),
			"node_id":    string(spawnedFrom.NodeID),
		}
	}

	return payload, truncate(finalSummary, maxOutputChars), subagent, nil
}

func (console *Console) persist(ctx context.Context) error {
	if console.store == nil {
		return nil
	}

	if err := console.store.Save(ctx, console.agent.Graph()); err != nil {
		return fmt.Errorf("persist graph: %w", err)
	}

	return nil
}

func (console *Console) resolvePath(value string) (string, string, error) {
	cleaned := filepath.Clean(value)
	if filepath.IsAbs(cleaned) {
		rel, err := filepath.Rel(console.workspaceRoot, cleaned)
		if err != nil {
			return "", "", fmt.Errorf("resolve absolute path: %w", err)
		}
		cleaned = rel
	}

	fullPath := filepath.Join(console.workspaceRoot, cleaned)
	relPath, err := filepath.Rel(console.workspaceRoot, fullPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", "", errors.New("path escapes workspace root")
	}

	return fullPath, filepath.ToSlash(relPath), nil
}

func (console *Console) resolveSearchRoot(pathPrefix string) (string, string, error) {
	if pathPrefix == "" {
		return console.workspaceRoot, ".", nil
	}

	fullPath, displayPath, err := console.resolvePath(pathPrefix)
	if err != nil {
		return "", "", err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("stat path prefix: %w", err)
	}
	if !info.IsDir() {
		return "", "", errors.New("path_prefix must be a directory")
	}

	return fullPath, displayPath, nil
}

func toolDeclarations() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "list_files",
				Description: "List files under the workspace or under a subdirectory.",
				ParametersJsonSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path_prefix": map[string]any{"type": "string", "description": "Optional subdirectory relative to the workspace root."},
						"limit":       map[string]any{"type": "integer", "description": "Maximum number of files to return."},
					},
				},
			},
			{
				Name:        "read_file",
				Description: "Read a specific line range from a text file in the workspace.",
				ParametersJsonSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "Workspace-relative file path."},
						"start_line": map[string]any{"type": "integer", "description": "1-based starting line."},
						"end_line":   map[string]any{"type": "integer", "description": "1-based ending line."},
					},
					"required": []string{"path"},
				},
			},
			{
				Name:        "grep_text",
				Description: "Search text files in the workspace for a substring and return matching lines.",
				ParametersJsonSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":       map[string]any{"type": "string", "description": "Case-insensitive search text."},
						"path_prefix": map[string]any{"type": "string", "description": "Optional subdirectory relative to the workspace root."},
						"limit":       map[string]any{"type": "integer", "description": "Maximum number of matches to return."},
					},
					"required": []string{"query"},
				},
			},
			{
				Name:        "spawn_subagent_search",
				Description: "Spawn a focused subagent session that performs a workspace search and returns a concise summary.",
				ParametersJsonSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question":    map[string]any{"type": "string", "description": "Question the subagent is answering."},
						"query":       map[string]any{"type": "string", "description": "Search text for the subagent to use."},
						"path_prefix": map[string]any{"type": "string", "description": "Optional subdirectory relative to the workspace root."},
						"limit":       map[string]any{"type": "integer", "description": "Maximum number of matches to inspect."},
					},
				},
			},
		},
	}}
}

var errLimitReached = errors.New("result limit reached")

func buildOutcome(call *genai.FunctionCall, payload any, output string, err error) toolOutcome {
	if err != nil {
		return toolOutcome{
			call:    call,
			name:    call.Name,
			payload: nil,
			output:  truncate(err.Error(), maxOutputChars),
			success: false,
		}
	}

	return toolOutcome{
		call:    call,
		name:    call.Name,
		payload: payload,
		output:  truncate(output, maxOutputChars),
		success: true,
	}
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".sgp-sessions", "node_modules":
		return true
	default:
		return false
	}
}

func isBinary(data []byte) bool {
	for _, value := range data {
		if value == 0 {
			return true
		}
	}

	return false
}

func contentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		data, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return fmt.Sprint(value)
		}

		return string(data)
	}
}

func firstCandidateContent(result *genai.GenerateContentResponse) *genai.Content {
	if result == nil || len(result.Candidates) == 0 {
		return nil
	}

	return result.Candidates[0].Content
}

func summarizeFunctionCalls(calls []*genai.FunctionCall, leadingText string) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		if call == nil {
			continue
		}
		parts = append(parts, describeCall(call))
	}

	if leadingText != "" {
		return truncate(leadingText+"\nTool plan: "+strings.Join(parts, "; "), maxOutputChars)
	}

	return truncate("Tool plan: "+strings.Join(parts, "; "), maxOutputChars)
}

func summarizeParallelOutcomes(outcomes []toolOutcome) string {
	parts := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		state := "ok"
		if !outcome.success {
			state = "failed"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", outcome.name, state))
	}

	return truncate("Parallel tool batch summary: "+strings.Join(parts, "; "), maxOutputChars)
}

func describeCall(call *genai.FunctionCall) string {
	if call == nil {
		return "<nil>"
	}

	if len(call.Args) == 0 {
		return call.Name + "()"
	}

	data, err := json.Marshal(call.Args)
	if err != nil {
		return call.Name + "(...)"
	}

	return fmt.Sprintf("%s(%s)", call.Name, string(data))
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}

	return value[:limit-3] + "..."
}

func stringArg(args map[string]any, key string, fallback string) string {
	if args == nil {
		return fallback
	}

	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}

	stringValue, ok := value.(string)
	if !ok {
		return fallback
	}

	return stringValue
}

func intArg(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}

	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}

	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float32:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return fallback
	}
}
