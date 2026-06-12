package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/cmd/launcher"
	"google.golang.org/adk/cmd/launcher/full"
	"google.golang.org/adk/model/gemini"

	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/liveagent"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/oacstream"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/sgpsession"
	jsonstore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/json"
)

const (
	defaultAppName        = "oac_sgp_agent"
	defaultCodingModel    = "gemini-2.5-flash"
	defaultOllamaModel    = "qwen2.5-coder:3b"
	defaultOllamaBaseURL  = "http://localhost:11434"
	defaultUserID         = "orchestrator"
	adkModeArg            = "adk"
	codingConsoleArg      = "coding-console"
	forceCodingConsoleKey = "RUN_CODING_CONSOLE"
	codingModelKey        = "CODING_MODEL"
	ollamaBaseURLKey      = "OLLAMA_HOST"
	ollamaModelKey        = "OLLAMA_MODEL"
	ollamaModelFileKey    = "OLLAMA_MODEL_FILE"
	ollamaAutoPullKey     = "OLLAMA_AUTO_PULL"
	codingSessionSubdir   = "coding-console"
	oacSessionSubdir      = "oac-harness"
	codingWorkspaceKey    = "CODING_WORKSPACE_ROOT"
	orchestratorURLKey    = "ORCHESTRATOR_URL"
	orchestratorTokenKey  = "ORCHESTRATOR_TOKEN"
	orchestratorCAKey     = "ORCHESTRATOR_CA_CERT"
	orchestratorCertKey   = "ORCHESTRATOR_CLIENT_CERT"
	orchestratorPrivKey   = "ORCHESTRATOR_CLIENT_KEY"
)

func main() {
	ctx := context.Background()
	orchestratorURL := strings.TrimSpace(os.Getenv(orchestratorURLKey))
	modeArg := ""
	if len(os.Args) > 1 {
		modeArg = strings.TrimSpace(os.Args[1])
	}

	switch {
	case orchestratorURL != "":
		runOACStream(ctx, orchestratorURL)
	case strings.TrimSpace(os.Getenv(forceCodingConsoleKey)) == "1":
		runCodingConsole(ctx)
	case modeArg == "" || modeArg == codingConsoleArg:
		runCodingConsole(ctx)
	case modeArg == adkModeArg:
		runADKLauncher(ctx)
	default:
		log.Fatalf("unknown mode %q; use %q or %q", modeArg, codingConsoleArg, adkModeArg)
	}
}

func runADKLauncher(ctx context.Context) {
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey == "" {
		log.Fatalf("GOOGLE_API_KEY is required for %q mode", adkModeArg)
	}

	model, err := gemini.NewModel(ctx, "gemini-3.1-flash-lite", &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		log.Fatalf("failed to create model: %v", err)
	}

	sessionDir := os.Getenv("SGP_SESSION_DIR")
	if sessionDir == "" {
		sessionDir = ".sgp-sessions"
	}
	sessionService, err := sgpsession.NewService(sessionDir)
	if err != nil {
		log.Fatalf("failed to create SGP session service: %v", err)
	}

	a, err := llmagent.New(llmagent.Config{
		Name:        "oac_sgp_agent",
		Model:       model,
		Description: "ADK agent with SGP-backed durable session graph storage.",
		Instruction: "You are a helpful execution agent. Keep responses precise and include assumptions when uncertain.",
	})
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}

	config := &launcher.Config{
		AgentLoader:    agent.NewSingleLoader(a),
		SessionService: sessionService,
	}

	args := os.Args[1:]
	if len(args) > 0 && args[0] == adkModeArg {
		args = args[1:]
	}

	l := full.NewLauncher()
	if err = l.Execute(ctx, config, args); err != nil {
		log.Fatalf("run failed: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

func runOACStream(ctx context.Context, orchestratorURL string) {
	runtime, err := newModelRuntime(ctx, nil, nil, false)
	if err != nil {
		log.Fatalf("failed to create harness model runtime: %v", err)
	}

	store, err := newHarnessStore(oacSessionSubdir)
	if err != nil {
		log.Fatalf("failed to create OAC harness store: %v", err)
	}

	workspaceRoot := strings.TrimSpace(os.Getenv(codingWorkspaceKey))
	if workspaceRoot == "" {
		workspaceRoot = defaultCodingWorkspaceRoot()
	}

	harness, err := liveagent.NewHarness(
		workspaceRoot,
		store,
		runtime.generator,
		runtime.modelName,
		"",
	)
	if err != nil {
		log.Fatalf("failed to create OAC harness: %v", err)
	}

	tlsCfg := oacstream.TLSConfig{
		CACertPath:     strings.TrimSpace(os.Getenv(orchestratorCAKey)),
		ClientCertPath: strings.TrimSpace(os.Getenv(orchestratorCertKey)),
		ClientKeyPath:  strings.TrimSpace(os.Getenv(orchestratorPrivKey)),
	}
	client, err := oacstream.NewConnectClientWithTLS(
		orchestratorURL,
		os.Getenv(orchestratorTokenKey),
		tlsCfg,
	)
	if err != nil {
		log.Fatalf("failed to create OAC stream client: %v", err)
	}

	log.Printf("running in OAC stream mode against %s", orchestratorURL)
	err = client.Run(
		ctx,
		func(callCtx context.Context, incoming oacstream.OrchestratorEnvelope) (oacstream.HarnessEnvelope, error) {
			if incoming.SessionEnd {
				if endErr := harness.EndSession(callCtx, incoming.SessionID); endErr != nil {
					return oacstream.HarnessEnvelope{
						SessionID: incoming.SessionID,
						Result: &oacstream.EventResult{
							Success:      false,
							ErrorMessage: endErr.Error(),
						},
					}, nil
				}
				return oacstream.HarnessEnvelope{
					SessionID: incoming.SessionID,
					Result:    &oacstream.EventResult{Success: true},
				}, nil
			}

			event := incoming.Event
			if event == nil {
				return oacstream.HarnessEnvelope{
					SessionID: incoming.SessionID,
					Result: &oacstream.EventResult{
						Success:      false,
						ErrorMessage: "missing event body",
					},
				}, nil
			}

			response, err := harness.HandleEvent(
				callCtx,
				incoming.SessionID,
				event.Channel,
				event.ContentType,
				event.Payload,
			)
			if err != nil {
				return oacstream.HarnessEnvelope{
					SessionID: incoming.SessionID,
					Result: &oacstream.EventResult{
						Success:      false,
						ErrorMessage: err.Error(),
					},
				}, nil
			}

			log.Printf(
				"processed OAC event session=%s channel=%s response=%q",
				incoming.SessionID,
				event.Channel,
				truncateForLog(response, 200),
			)

			return oacstream.HarnessEnvelope{
				SessionID: incoming.SessionID,
				Result:    &oacstream.EventResult{Success: true},
			}, nil
		},
	)
	if err != nil {
		log.Fatalf("oac stream mode failed: %v", err)
	}
}

func runCodingConsole(ctx context.Context) {
	input := bufio.NewReader(os.Stdin)
	runtime, err := newModelRuntime(ctx, input, os.Stdout, true)
	if err != nil {
		log.Fatalf("failed to create coding-console model runtime: %v", err)
	}

	store, err := newHarnessStore(codingSessionSubdir)
	if err != nil {
		log.Fatalf("failed to create coding-console store: %v", err)
	}

	workspaceRoot := strings.TrimSpace(os.Getenv(codingWorkspaceKey))
	if workspaceRoot == "" {
		workspaceRoot = defaultCodingWorkspaceRoot()
	}

	console, err := liveagent.NewConsole(workspaceRoot, store, "")
	if err != nil {
		log.Fatalf("failed to create coding console: %v", err)
	}
	defer func() {
		if closeErr := console.Close(context.Background()); closeErr != nil {
			log.Printf("failed to close coding console cleanly: %v", closeErr)
		}
	}()

	if runtime.hosted {
		log.Printf(
			"running coding-console mode against %s with session %s",
			console.WorkspaceRoot(),
			console.SessionID(),
		)
	} else {
		log.Printf("running coding-console mode with local Ollama model %s at %s", runtime.modelName, runtime.ollamaBaseURL)
	}
	if err := console.Run(ctx, input, os.Stdout, runtime.generator, runtime.modelName); err != nil {
		log.Fatalf("coding-console failed: %v", err)
	}
}

type modelRuntime struct {
	generator     liveagent.ModelGenerator
	modelName     string
	hosted        bool
	ollamaBaseURL string
}

func newModelRuntime(
	ctx context.Context,
	input *bufio.Reader,
	output *os.File,
	allowPrompt bool,
) (modelRuntime, error) {
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	if apiKey != "" {
		modelName := strings.TrimSpace(os.Getenv(codingModelKey))
		if modelName == "" {
			modelName = defaultCodingModel
		}

		client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
		if err != nil {
			return modelRuntime{}, fmt.Errorf("create genai client: %w", err)
		}

		return modelRuntime{generator: client.Models, modelName: modelName, hosted: true}, nil
	}

	ollamaBaseURL := strings.TrimSpace(os.Getenv(ollamaBaseURLKey))
	if ollamaBaseURL == "" {
		ollamaBaseURL = defaultOllamaBaseURL
	}

	modelName := resolveOllamaModel()
	ollamaClient := liveagent.NewOllamaClient(ollamaBaseURL)
	if allowPrompt {
		if input == nil || output == nil {
			return modelRuntime{}, errors.New("interactive ollama setup requires stdin and stdout")
		}
		if err := liveagent.PromptBeforePull(ctx, input, output, ollamaClient, modelName); err != nil {
			return modelRuntime{}, err
		}
	} else {
		exists, err := ollamaClient.HasModel(ctx, modelName)
		if err != nil {
			return modelRuntime{}, err
		}
		if !exists {
			if strings.TrimSpace(os.Getenv(ollamaAutoPullKey)) == "1" {
				if err = ollamaClient.PullModel(ctx, modelName, os.Stdout); err != nil {
					return modelRuntime{}, err
				}
			} else {
				return modelRuntime{}, fmt.Errorf("ollama model %s is not installed; preinstall it in the harness or set %s=1", modelName, ollamaAutoPullKey)
			}
		}
	}

	return modelRuntime{
		generator:     liveagent.NewOllamaGenerator(ollamaBaseURL, modelName),
		modelName:     modelName,
		ollamaBaseURL: ollamaBaseURL,
	}, nil
}

func newHarnessStore(subdir string) (*jsonstore.JSONFileStore, error) {
	sessionDir := os.Getenv("SGP_SESSION_DIR")
	if sessionDir == "" {
		sessionDir = ".sgp-sessions"
	}

	return jsonstore.NewJSONFileStore(filepath.Join(sessionDir, subdir))
}

func truncateForLog(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}

	if limit <= 3 {
		return value[:limit]
	}

	return value[:limit-3] + "..."
}

func defaultCodingWorkspaceRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}

	if filepath.Base(cwd) == "adk-oac-sgp-agent" && filepath.Base(filepath.Dir(cwd)) == "examples" {
		return filepath.Clean(filepath.Join(cwd, "../.."))
	}

	return cwd
}

func resolveOllamaModel() string {
	if modelFile := strings.TrimSpace(os.Getenv(ollamaModelFileKey)); modelFile != "" {
		data, err := os.ReadFile(modelFile)
		if err == nil {
			if model := strings.TrimSpace(string(data)); model != "" {
				return model
			}
		}
	}

	if model := strings.TrimSpace(os.Getenv(ollamaModelKey)); model != "" {
		return model
	}

	return defaultOllamaModel
}
