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

const defaultSystemPrompt = "You are a helpful assistant with access to file reading. Use the read_file tool to read files when needed."

func main() {
	model := flag.String("model", "llama3.2", "Ollama model name")
	sessionDir := flag.String("session-dir", ".sgp-sessions", "Directory to store session graphs")
	sessionID := flag.String("session-id", "", "Resume an existing session (empty = new)")
	ollamaURL := flag.String("ollama-url", "http://localhost:11434", "Ollama base URL")
	system := flag.String("system", defaultSystemPrompt, "System prompt")
	var peers stringSlice
	flag.Var(&peers, "peer", "Peer harness as path=description (repeatable)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	peersDesc := buildPeersDesc(peers)
	systemPrompt := *system
	if peersDesc != "" {
		systemPrompt += "\n\n" + peersDesc
	}
	h, sid, err := newHarness(
		*sessionDir,
		*sessionID,
		*ollamaURL,
		*model,
		systemPrompt,
		"Tools: read_file (read file contents), teleport (switch harness).",
		peersDesc,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	absDir, _ := filepath.Abs(*sessionDir)
	fmt.Printf("session:  %s\nsnapshot: %s/%s.json\n\n", sid, absDir, sid)

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
