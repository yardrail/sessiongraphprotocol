package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	cayleygraph "github.com/cayleygraph/cayley/graph"
	_ "github.com/cayleygraph/cayley/graph/bolt"
	_ "github.com/cayleygraph/cayley/graph/memstore"
	cayleystore "github.com/restrukt-ai/sessiongraphprotocol/pkg/store/cayley"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/sgp"
	"github.com/restrukt-ai/sessiongraphprotocol/pkg/store/sgpd"
)

type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

func buildPeersDesc(peers []string) string {
	if len(peers) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Available harnesses (reachable via teleport):")
	for _, p := range peers {
		idx := strings.IndexByte(p, '=')
		if idx < 0 {
			sb.WriteString("\n- " + p)
		} else {
			sb.WriteString("\n- " + p[:idx] + ": " + p[idx+1:])
		}
	}
	return sb.String()
}

const defaultSystemPrompt = "You are a helpful math assistant. Use the calculate tool to evaluate mathematical expressions."

func main() {
	// OAC mode: ORCHESTRATOR_ADDR is set by the orchestrator at container start.
	if oacCfg := loadOACConfig(); oacCfg != nil {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		if err := runOACMode(ctx, oacCfg); err != nil {
			fmt.Fprintf(os.Stderr, "oac error: %v\n", err)
			os.Exit(1)
		}

		return
	}

	model := flag.String("model", "llama3.2", "Ollama model name")
	sessionDir := flag.String(
		"session-dir",
		".sgp-sessions",
		"Directory to store session graphs (file store)",
	)
	sessionID := flag.String("session-id", "", "Resume an existing session (empty = new)")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	system := flag.String("system", defaultSystemPrompt, "System prompt")
	sgpdURL := flag.String(
		"sgpd-url",
		"",
		"sgpd server URL (e.g. http://localhost:9090); uses sgpd instead of local file store",
	)
	sgpdToken := flag.String("sgpd-token", "", "Bearer token for sgpd harness service")
	var peers stringSlice
	flag.Var(&peers, "peer", "Peer harness as path=description (repeatable)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var store sgp.Store
	if *sgpdURL != "" {
		store = sgpd.NewClient(*sgpdURL, *sgpdToken)
	} else {
		storePath := filepath.Join(*sessionDir, "cayley.db")
		if err := os.MkdirAll(*sessionDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error creating session dir: %v\n", err)
			os.Exit(1)
		}
		qs, err := cayleygraph.NewQuadStore("bolt", storePath, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening store: %v\n", err)
			os.Exit(1)
		}
		store = cayleystore.New(qs)
	}

	peersDesc := buildPeersDesc(peers)
	systemPrompt := *system
	if peersDesc != "" {
		systemPrompt += "\n\n" + peersDesc
	}
	h, sid, err := newHarness(
		store,
		*sessionDir,
		*sessionID,
		*ollamaURL,
		*model,
		systemPrompt,
		"Tools: calculate (math expressions), teleport (switch harness).",
		peersDesc,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	h.sgpdURL = *sgpdURL
	h.sgpdToken = *sgpdToken

	if *sgpdURL != "" {
		fmt.Printf("session: %s (sgpd: %s)\n\n", sid, *sgpdURL)
	} else {
		absDir, _ := filepath.Abs(*sessionDir)
		fmt.Printf("session:  %s\nstore:    %s/cayley.db\n\n", sid, absDir)
	}

	selfPath, _ := os.Executable()

	arrivalResponse, arrived, err := h.handleArrival(ctx, selfPath)
	if errors.Is(err, errTeleported) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "arrival error: %v\n", err)
		os.Exit(1)
	}
	if arrived {
		fmt.Println(arrivalResponse)
		fmt.Println()
	}

	teleported := false
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == ":quit" || line == ":exit" {
			break
		}

		response, err := h.handleTurn(ctx, line)
		if errors.Is(err, errTeleported) {
			teleported = true
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		fmt.Println(response)
		fmt.Println()
	}

	if teleported {
		os.Exit(0)
	}
	h.close(context.Background())
}
