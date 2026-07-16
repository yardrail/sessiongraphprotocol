# Session Graph Protocol — Session Model

## 1. Overview

A **session** is an agent's lifetime, not a single conversation run.

The original SGP model treated a session as one chat exchange: create session, append messages, end session. Persistence was convenient but secondary. The new model treats the session as a durable, ever-growing graph that accumulates everything the agent experiences, remembers, learns, and becomes.

Contrast:

| Old model | New model |
|---|---|
| Session = one chat run | Session = agent lifetime |
| End session when chat ends | Session ends only on explicit shutdown |
| History compacted via `Rewrite()` | Compaction via sleep cycles with typed edges |
| All nodes are "experience" | Nodes have kinds: experience, memory, skill, identity, sleep |
| Edges are implicit parent links | Edges are typed and optionally weighted |

The graph never shrinks. Nodes are never deleted. Archived nodes remain reachable via `distilled_from` traversal. The inference view presented to the LLM is a curated window into the graph, not the full graph.

---

## 2. Session Lifecycle

```
[create] → [active] ←→ [sleep cycle] → [end]
```

1. **Create**: `CreateSession` persists session metadata. A system-prompt node is appended. The agent is live.
2. **Active**: The agent handles turns via `handleTurn` / `runInferenceLoop`. Each turn appends experience nodes. Tool calls and results are experience nodes.
3. **Sleep cycle**: Triggered by harness policy (e.g., after N turns, on idle timeout). The subconscious harness runs light-sleep and/or REM-sleep passes that write memory, skill, and sleep nodes back into the session graph.
4. **End**: `EndSession` records `EndReason` and `terminalNodeID`. Reasons include `complete`, `error`, `timeout`, `interrupted`. An ended session is read-only but fully queryable.

Session status is either `open` or `closed`. Closed sessions can still be read via `LoadGraph`, `GetLineage`, `GetSessionGraph`.

---

## 3. Node Kinds

| Kind | Purpose | Content struct | Typical incoming edges |
|---|---|---|---|
| `experience` | Raw inference turn: system prompt, user message, assistant reply, tool call/result | none (Message field carries content) | `parent` from previous experience node |
| `memory` | Distilled semantic memory extracted from experiences | `MemoryContent` | `parent` from sleep node, `distilled_from` experience nodes |
| `skill` | Reusable procedure or capability the agent has learned | `SkillContent` | `parent` from sleep node, `procedural_of` experience nodes |
| `identity` | Agent's current self-model: traits, values, goals | `IdentityContent` | `parent` from prior identity node, `evolved_from` prior identity |
| `sleep` | Marker node for a sleep cycle boundary | `SleepContent` | `parent` from head experience node |

`EffectiveKind()` returns `experience` when `Kind == ""` for backward compatibility with pre-typed nodes.

### Content structs

```go
type MemoryContent struct {
    Summary    string   `json:"summary"`
    Tags       []string `json:"tags,omitempty"`
    Importance float64  `json:"importance"` // 0.0–1.0
}

type SkillContent struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Procedure   string `json:"procedure"`
}

type IdentityContent struct {
    Traits []string `json:"traits,omitempty"`
    Values []string `json:"values,omitempty"`
    Goals  []string `json:"goals,omitempty"`
}

type SleepContent struct {
    Kind SleepKind `json:"kind"` // "light" | "rem"
}
```

---

## 4. Edge Kinds

| Kind | Direction | Weighted | Semantics |
|---|---|---|---|
| `parent` | node → parent | no | Primary DAG backbone; canonical lineage for resume |
| `distilled_from` | memory → experience(s) | no | Which experiences a memory was extracted from |
| `associated_with` | memory → memory | yes | Semantic relatedness; weight = similarity score (0–1) |
| `recalled_in` | memory → experience | yes | This memory was injected into this inference turn; weight = relevance score |
| `evolved_from` | identity → prior identity | no | Identity node supersedes a prior identity node |
| `procedural_of` | skill → experience(s) | no | Which experiences the skill was derived from |
| `archives` | memory/sleep → experience(s) | no | These experience nodes are superseded; harness should set `Archived=true` on the target |

Weighted edges (`associated_with`, `recalled_in`) are stored via reification in the Cayley quad store: a synthetic edge IRI `edge:{fromID}:{kind}:{toID}` carries the weight as a separate quad. Unweighted edges are stored directly as subject–predicate–object triples.

---

## 5. Inference View

The LLM never sees the full session graph. The harness constructs a view:

1. **Active experience chain**: nodes with `EffectiveKind() == experience` and `Archived == false`, from session root to current head, via canonical `parent` lineage (`ResumeNodes(headID)`).
2. **Active identity node** (optional): the most recent `identity` node. Injected as an additional system message or prepended context.
3. **Recalled memories** (optional): memory nodes retrieved by semantic search, injected as tool results or context messages.

Archived experience nodes are excluded from the inference view but remain in the graph. Memory, skill, sleep, and identity nodes are not sent directly as conversation messages — they influence the view through injection by the harness.

The `toOllamaMessages` filter in example harnesses encodes this rule:

```go
if node.EffectiveKind() != sgp.NodeKindExperience || node.Archived {
    continue
}
```

---

## 6. Sleep Cycle

Sleep replaces the old `Rewrite()` / `synthesized_from` compaction mechanism. Where `Rewrite()` retroactively replaced history in-place, sleep creates new nodes that describe what was learned, leaving the original experience nodes intact.

### Light sleep

1. Subconscious harness reads the un-distilled experience chain since the last sleep node.
2. Calls LLM to extract memories from that chain.
3. Writes `NodeKindMemory` nodes with `MemoryContent`, `distilled_from` edges to source experiences, and `parent` edge to the sleep node.
4. Sets `Archived = true` on low-significance experience nodes (those that have been fully distilled).
5. Writes a `NodeKindSleep{Kind: "light"}` marker node.

### REM sleep

1. Subconscious harness reads all memory nodes for the session.
2. Calls LLM (or embedding model) to score pairwise similarity.
3. Writes `associated_with` edges (weighted) between related memory nodes.
4. May write updated `identity` node if traits/goals have shifted.
5. Writes a `NodeKindSleep{Kind: "rem"}` marker node.

Sleep depth can be tuned by harness policy. A minimal harness skips sleep entirely; memories accumulate only when explicitly triggered.

---

## 7. Subconscious

The subconscious is a **stateless** harness: it has no session of its own. It receives a session ID, loads the graph, runs templated LLM calls against it, and writes results back into the agent's session graph.

Key properties:
- No `handleTurn` / interactive loop — pure batch processing.
- Writes only non-experience node kinds (memory, skill, identity, sleep).
- Uses `AppendTypedNode` to attach new nodes with correct kind and edges.
- Can be triggered by the agent harness post-turn or run as a separate process/cron.

The subconscious writes to the agent's session, not a separate session. This means the agent will see its own memories, skills, and identity nodes on the next `LoadGraph`.

---

## 8. Perfect Recall

Nodes are **never deleted**. The append-only graph is the source of truth.

- Archived nodes are excluded from the active inference view but are fully readable.
- `distilled_from` edges trace from a memory back to the original experience nodes that produced it.
- `GetLineage(nodeID)` walks the canonical `parent` chain from root to any node.
- `GetSessionGraph(sessionID)` returns the full node+edge set for offline analysis.

Future work: lazy-loading will allow partial graph views for sessions with millions of nodes, loading only the inference-view subgraph on demand.

---

## 9. Wire Protocol

Each node kind maps to a wire event via `ClassifyEvent` / `SynthesizeEvents`:

| Situation | Event kind |
|---|---|
| Any node appended (all kinds) | `NodeAppended` |
| Session started | `SessionStarted` |
| Session ended | `SessionEnded` |

`EventKindHistoryRewritten` has been removed. There is no event for compaction because compaction no longer happens — sleep creates new nodes instead of rewriting old ones.

The `sgpd` daemon emits `NodeAppended` events for all node kinds. Consumers (e.g., watchers, indexers) inspect `node.Kind` to route accordingly.

---

## 10. Harness Integration Checklist

Steps for a harness to participate in the full session model:

- [ ] **Session create/resume**: pass `--session-id ""` to create; pass an existing ID to resume via `LoadGraph`.
- [ ] **Inference view construction**: use `graph.ResumeNodes(headID)` and filter to `EffectiveKind() == experience && !Archived`.
- [ ] **Inject identity context** (optional): load the latest `NodeKindIdentity` node and prepend its content to the system prompt.
- [ ] **Memory recall injection** (optional): before each turn, search for relevant memories and inject as context.
- [ ] **Sleep trigger conditions**: define a policy — e.g., every 20 experience nodes, or after each session pause. Call subconscious harness with the session ID.
- [ ] **Typed node writes**: use `graph.AppendTypedNode` when writing non-experience nodes (memory, skill, identity, sleep).
- [ ] **Archived flag**: set `node.Archived = true` before calling `store.WriteNode` on experience nodes that have been fully distilled into memories.
- [ ] **Teleport compatibility**: `spawnHandoff` passes `--session-id` so the destination harness resumes the same lifetime graph.
- [ ] **End session**: call `harness.close(ctx)` to record `EndReasonComplete` when the agent shuts down cleanly.
