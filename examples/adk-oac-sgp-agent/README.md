# ADK OAC SGP Agent Example

This example wires ADK runtime sessions to Session Graph Protocol (SGP) JSON snapshots.

## What it does

- Runs an ADK `llmagent` with Gemini.
- Injects a custom ADK `session.Service` implementation in `sgpsession`.
- Persists each session's canonical transcript as an SGP graph via `JSONFileStore`.
- Keeps ADK-compatible state semantics (`app:*`, `user:*`, and session-local keys).
- Implements direct OAC v1alpha2 Connect stream handling in-process (no sidecar).
- Includes a `codingagent` proof of concept showing SGP-based subagents, failed-tool pruning, and parallel-tool summarization.

## Coding Agent Proof Of Concept

The `codingagent` package is a focused harness-level proof of concept for the kind of behavior described in your boss's note.

It demonstrates three SGP-native coding-agent patterns:

- Subagents: creates child session graphs using `WithSpawnedFrom(...)` so subagent work has explicit provenance back to the parent node.
- Failed tool call pruning: uses `Graph.Rewrite(...)` to remove a failed tool result from the canonical resume path while preserving it in `synthesized_from` audit history.
- Parallel tool summarization: models sibling tool results as parallel leaves, then rewrites them into one assistant summary node for future resume/inference.

Code lives in:

- `codingagent/agent.go`
- `codingagent/agent_test.go`

## Run locally

```bash
cd examples/adk-oac-sgp-agent
go mod tidy
go run . console
```

Required environment variables:

- `GOOGLE_API_KEY`: Gemini API key for the hosted model path. If omitted, the console uses Ollama locally.

Optional environment variables:

- `SGP_SESSION_DIR`: directory for SGP JSON graph files. Default: `.sgp-sessions`.

### Live coding console

The example binary now includes a live `coding-console` mode. It uses Gemini when `GOOGLE_API_KEY` is set and falls back to Ollama when it is not.

It provides repository-scoped local tools for:

- listing files
- reading line ranges from files
- grepping text in the workspace
- spawning a focused search subagent with explicit `spawned_from` provenance

The harness records each turn in SGP and applies the proof-of-concept rewrite behaviors during live use:

- a single failed tool call is rewritten out of the canonical resume path
- multiple tool calls in one turn are summarized into one canonical assistant node
- spawned subagent searches are persisted as independent SGP sessions with provenance back to the parent node

Run it like this:

```bash
cd examples/adk-oac-sgp-agent
go run .
```

Useful optional environment variables:

- `CODING_WORKSPACE_ROOT`: workspace root the live tools should inspect. When omitted from the example directory, the binary defaults to the repository root.
- `CODING_MODEL`: Gemini model for the hosted path. Default: `gemini-2.5-flash`.
- `OLLAMA_HOST`: Ollama base URL for the local path. Default: `http://localhost:11434`.
- `OLLAMA_MODEL`: Ollama model for the local path. Default: `qwen2.5-coder:3b`.
- `OLLAMA_MODEL_FILE`: path to a text file containing the Ollama model name. If set, this overrides `OLLAMA_MODEL` and is the easiest way to switch models without editing commands.
- `SGP_SESSION_DIR`: base directory for persisted SGP snapshots. The console stores its sessions under `coding-console/` inside that base directory.

If the local Ollama model is not already installed, the console will prompt before pulling it.

If you prefer an explicit mode switch, `go run . coding-console` is still accepted, but plain `go run .` is now the default developer path.

### ADK launcher mode

The older hosted ADK launcher path is now explicit:

```bash
cd examples/adk-oac-sgp-agent
GOOGLE_API_KEY=... go run . adk
```

This keeps the local developer workflow separate from the hosted Gemini workflow.

To update the local model later, change the contents of the file referenced by `OLLAMA_MODEL_FILE` or update `OLLAMA_MODEL` if you prefer env vars.

Inside the console, use `:help`, `:session`, and `:quit` for basic control.

### OAC stream mode

When `ORCHESTRATOR_URL` is set, the binary runs in strict OAC stream mode and initiates an
outbound Connect bidirectional stream to that endpoint.

In this mode, the model lives inside the harness. The harness consumes each OAC event,
routes it through the same liveagent session engine used by the local coding console,
persists the resulting SGP graph under the orchestrator `session_id`, and then acknowledges
 delivery back over the OAC stream.

Optional auth input:

- `ORCHESTRATOR_TOKEN`: bearer token used as `Authorization: Bearer ...`.
- `ORCHESTRATOR_CA_CERT`: CA certificate path for TLS validation.
- `ORCHESTRATOR_CLIENT_CERT`: client certificate path for optional mTLS.
- `ORCHESTRATOR_CLIENT_KEY`: client private key path for optional mTLS.

Model selection in OAC stream mode follows the same rules as local console mode:

- `GOOGLE_API_KEY` switches the harness to the hosted Gemini path.
- otherwise the harness uses local Ollama via `OLLAMA_HOST` and `OLLAMA_MODEL` or `OLLAMA_MODEL_FILE`.
- for non-interactive harness startup, missing Ollama models are not downloaded unless `OLLAMA_AUTO_PULL=1` is set.

By default, OAC stream sessions are stored under `oac-harness/` inside `SGP_SESSION_DIR`.

## OAC packaging

The included Dockerfile includes OAC labels and an event schema file:

- `org.openagentcontainers.version`
- `org.openagentcontainers.orchestrator.*`
- `org.openagentcontainers.events.*`
- `org.openagentcontainers.inference.*`

Build image:

```bash
docker build -t adk-oac-sgp-agent .
```

Auth labels default to no-auth for local development.

For production-style auth labeling, add the bearer token env declaration at build time:

```bash
docker build \
	--label org.openagentcontainers.orchestrator.bearer.token.env=ORCHESTRATOR_TOKEN \
	-t adk-oac-sgp-agent:secure .
```

## Compliance notes

- SGP compliance: session transcript history is persisted as immutable append-only SGP nodes.
- OAC artifact compliance: image metadata labels and schema are included.
- Auth posture: default image labels are no-auth for local development only.
- OAC runtime compliance: when `ORCHESTRATOR_URL` is present, the service uses the normative bidirectional stream envelope shape from the spec.
