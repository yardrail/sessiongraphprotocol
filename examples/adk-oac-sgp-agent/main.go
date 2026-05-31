package main

import (
	"bufio"
	"context"
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

	sgp "github.com/restrukt-ai/sessiongraphprotocol"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/liveagent"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/oacstream"
	"github.com/restrukt-ai/sessiongraphprotocol/examples/adk-oac-sgp-agent/sgpsession"
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
	codingSessionSubdir   = "coding-console"
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
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
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

	_, err = llmagent.New(llmagent.Config{
		Name:        "oac_sgp_agent",
		Model:       model,
		Description: "ADK agent with SGP-backed durable session graph storage.",
		Instruction: "You are a helpful execution agent. Keep responses precise and include assumptions when uncertain.",
	})
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}

	tlsCfg := oacstream.TLSConfig{
		CACertPath:     strings.TrimSpace(os.Getenv(orchestratorCAKey)),
		ClientCertPath: strings.TrimSpace(os.Getenv(orchestratorCertKey)),
		ClientKeyPath:  strings.TrimSpace(os.Getenv(orchestratorPrivKey)),
	}
	client, err := oacstream.NewConnectClientWithTLS(orchestratorURL, os.Getenv(orchestratorTokenKey), tlsCfg)
	if err != nil {
		log.Fatalf("failed to create OAC stream client: %v", err)
	}

	log.Printf("running in OAC stream mode against %s", orchestratorURL)
	err = client.Run(ctx, func(callCtx context.Context, incoming oacstream.OrchestratorEnvelope) (oacstream.HarnessEnvelope, error) {
		if incoming.SessionEnd {
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

		err = sessionService.IngestOrchestratorEvent(
			callCtx,
			defaultAppName,
			defaultUserID,
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

		return oacstream.HarnessEnvelope{
			SessionID: incoming.SessionID,
			Result:    &oacstream.EventResult{Success: true},
		}, nil
	})
	if err != nil {
		log.Fatalf("oac stream mode failed: %v", err)
	}
}

func runCodingConsole(ctx context.Context) {
	input := bufio.NewReader(os.Stdin)
	apiKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
	modelName := ""

	var generator liveagent.ModelGenerator
	if apiKey != "" {
		modelName = strings.TrimSpace(os.Getenv(codingModelKey))
		if modelName == "" {
			modelName = defaultCodingModel
		}

		client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
		if err != nil {
			log.Fatalf("failed to create genai client: %v", err)
		}
		generator = client.Models
	} else {
		ollamaBaseURL := strings.TrimSpace(os.Getenv(ollamaBaseURLKey))
		if ollamaBaseURL == "" {
			ollamaBaseURL = defaultOllamaBaseURL
		}

		modelName = resolveOllamaModel()

		ollamaClient := liveagent.NewOllamaClient(ollamaBaseURL)
		if err := liveagent.PromptBeforePull(ctx, input, os.Stdout, ollamaClient, modelName); err != nil {
			log.Fatalf("ollama setup failed: %v", err)
		}

		generator = liveagent.NewOllamaGenerator(ollamaBaseURL, modelName)
		log.Printf("running coding-console mode with local Ollama model %s at %s", modelName, ollamaBaseURL)
	}

	sessionDir := os.Getenv("SGP_SESSION_DIR")
	if sessionDir == "" {
		sessionDir = ".sgp-sessions"
	}

	store, err := sgp.NewJSONFileStore(filepath.Join(sessionDir, codingSessionSubdir))
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

	if apiKey != "" {
		log.Printf("running coding-console mode against %s with session %s", console.WorkspaceRoot(), console.SessionID())
	}
	if err := console.Run(ctx, input, os.Stdout, generator, modelName); err != nil {
		log.Fatalf("coding-console failed: %v", err)
	}
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
