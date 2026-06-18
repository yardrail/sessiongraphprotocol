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
	_ "github.com/cayleygraph/cayley/graph/kv/bolt"
	cayley_kv "github.com/cayleygraph/cayley/graph/kv"
	_ "github.com/cayleygraph/cayley/graph/memstore"
	hidalgo_flat "github.com/hidal-go/hidalgo/kv/flat"
	hidalgo_leveldb "github.com/hidal-go/hidalgo/kv/flat/leveldb"
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

// openLocalStore opens (or creates) a Cayley-backed sgp.Store for the given backend type.
// Supported types: mem, bolt, leveldb.
func openLocalStore(storeType, dir string) (sgp.Store, string, error) {
	switch storeType {
	case "mem":
		qs, err := cayleygraph.NewQuadStore("memstore", "", nil)
		if err != nil {
			return nil, "", err
		}
		return cayleystore.New(qs), "in-memory (memstore)", nil

	case "bolt":
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, "", err
		}
		path := filepath.Join(dir, "cayley.bolt")
		qs, err := cayleygraph.NewQuadStore("bolt", path, nil)
		if err != nil {
			return nil, "", err
		}
		absPath, _ := filepath.Abs(path)
		return cayleystore.New(qs), absPath + " (bolt)", nil

	case "leveldb":
		path := filepath.Join(dir, "cayley.ldb")
		if err := os.MkdirAll(path, 0o755); err != nil {
			return nil, "", err
		}
		flatDB, err := hidalgo_leveldb.OpenPath(path)
		if err != nil {
			return nil, "", fmt.Errorf("leveldb open: %w", err)
		}
		kvDB := hidalgo_flat.Upgrade(flatDB)
		if initErr := cayley_kv.Init(kvDB, nil); initErr != nil && !errors.Is(initErr, cayleygraph.ErrDatabaseExists) {
			return nil, "", fmt.Errorf("leveldb init: %w", initErr)
		}
		qs, err := cayley_kv.New(kvDB, nil)
		if err != nil {
			return nil, "", fmt.Errorf("leveldb new: %w", err)
		}
		absPath, _ := filepath.Abs(path)
		return cayleystore.New(qs), absPath + " (leveldb)", nil

	default:
		return nil, "", fmt.Errorf("unknown store %q: use mem, bolt, or leveldb", storeType)
	}
}

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
		"Directory for session data (ignored for --store=mem)",
	)
	sessionID := flag.String("session-id", "", "Resume an existing session (empty = new)")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	system := flag.String("system", defaultSystemPrompt, "System prompt")
	storeType := flag.String("store", "bolt", "Local backend: mem, bolt, leveldb (ignored when --sgpd-url is set)")
	sgpdURL := flag.String(
		"sgpd-url",
		"",
		"sgpd server URL (e.g. http://localhost:9090); uses sgpd instead of local store",
	)
	sgpdToken := flag.String("sgpd-token", "", "Bearer token for sgpd harness service")
	var peers stringSlice
	flag.Var(&peers, "peer", "Peer harness as path=description (repeatable)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var store sgp.Store
	var storePath string

	if *sgpdURL != "" {
		store = sgpd.NewClient(*sgpdURL, *sgpdToken)
		storePath = *sgpdURL + " (sgpd)"
	} else {
		var err error
		store, storePath, err = openLocalStore(*storeType, *sessionDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening store: %v\n", err)
			os.Exit(1)
		}
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

	fmt.Printf("session:  %s\nstore:    %s\n\n", sid, storePath)

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
