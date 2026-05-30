# Session Graph Protocol (SGP)

SGP defines a standard for representing, emitting, and resuming arbitrarily complex agent sessions.
It models a session as an append-only directed acyclic graph (DAG) of messages, supporting linear
progression, parallel branching, fan-in merges, history rewrites, and subagent sessions.

---

## Core Concepts

### Message (Node)

The atomic unit of the graph is a message: a single entry in the inference message array (system,
user, assistant, or tool role). Each message is a node in the graph. This is intentionally
finer-grained than a full request/response turn — it enables resumption from within a
multi-tool-call sequence without replaying completed work.

### Two Classes of Edges

Edges are split into two classes with distinct semantics:

**Canonical edges** (`parent_ids`) define the session as it is. Following `parent_ids` from HEAD to
the root and assembling the messages in order gives you the exact message history required to resume
the session. These are the only edges that matter for resumption.

**Audit edges** (`synthesized_from`, `spawned_from`) are non-canonical. They record what actually
happened — which branches were explored, which were merged into a rewrite, which subagent produced
a result. They are preserved permanently for observability and retroactive querying but are never
followed during resumption.

### Immutability

Nodes are immutable once emitted. There are no update events. The only way to "change" history is
to emit a new node (`history.rewritten`) that establishes a new canonical path forward. Prior nodes
remain intact.

### Resumption from Any Node

Because the graph is fully preserved, any node can serve as a resumption point. Traverse
`parent_ids` from that node to the root, collect the messages in order, and submit that sequence as
the inference context. Resuming from a non-HEAD node implicitly creates a new branch.

**Dangling leaf nodes:** A leaf node with no child is the normal state for HEAD, but it also occurs
when a container is killed while waiting for an inference response. On resume, the harness detects
a leaf whose role implies a pending response (e.g. a `user` or `tool` message with no subsequent
`assistant` child) and re-submits the inference request from that point.

### Retroactive Data Structure

The session graph is a **fully retroactive persistent data structure**. All versions of the
session — including non-canonical branches and rewritten history — are permanently preserved. This
means you can both modify the past (by emitting a `history.rewritten` node that establishes a new
canonical path from any prior point) and query any past state (by following `parent_ids` from any
node, combined with timestamps to resolve concurrent branches).

Immutability is what makes this possible: because existing nodes are never modified, every
historical state remains accessible. A rewrite does not destroy the branches it replaces — it adds
a new node that redirects the canonical path, leaving the old nodes intact as audit history.

---

## Data Model

### Node

```json
{
  "id": "<uuid>",
  "session_id": "<uuid>",
  "timestamp": "<rfc3339>",
  "parent_ids": ["<uuid>"],
  "synthesized_from": ["<uuid>"],
  "message": {
    "role": "system|user|assistant|tool",
    "content": "..."
  },
  "metadata": {
    "model": "...",
    "provider": "..."
  }
}
```

| Field              | Required | Description                                                                                                                                   |
| ------------------ | -------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `id`               | yes      | UUID, unique per node                                                                                                                         |
| `session_id`       | yes      | UUID of the session this node belongs to                                                                                                      |
| `timestamp`        | yes      | Wall-clock time the message was received or produced (RFC 3339)                                                                               |
| `parent_ids`       | yes      | Canonical parents. Empty only for the root node. Single entry for linear progression. Multiple entries for merge nodes.                        |
| `synthesized_from` | no       | Audit edge. Node IDs whose content was aggregated to produce this node. Used for rewrites and fan-in merges.                                   |
| `message`          | yes      | The inference message: role and content. Content may be a string or an array of content blocks per the inference provider's message schema.   |
| `metadata`         | no       | Harness-defined metadata. Typically populated on `assistant` nodes (model, provider). Omitted or empty on other roles.                        |

### Session

```json
{
  "id": "<uuid>",
  "timestamp": "<rfc3339>",
  "spawned_from": {
    "session_id": "<uuid>",
    "node_id": "<uuid>"
  }
}
```

`spawned_from` is omitted for root sessions. For subagent sessions it references the exact node in
the parent session that triggered the spawn.

---

## Event Stream

The harness emits a fine-grained event stream as the session progresses. Events are append-only and
ordered by emission time.

### Event Types

| Event               | Emitted when                                              | Key fields                                      |
| ------------------- | --------------------------------------------------------- | ----------------------------------------------- |
| `session.start`     | A session begins                                          | `session_id`, `spawned_from` (subagents only)   |
| `node.appended`     | A message is added to the session                         | `node` (full node object)                       |
| `history.rewritten` | The harness aggregates branches and redirects the trunk   | `node` with `parent_ids` + `synthesized_from`   |
| `session.ended`     | A session terminates                                      | `session_id`, `terminal_node_id`                |

---

#### `session.start`

```json
{
  "event": "session.start",
  "session_id": "<uuid>",
  "timestamp": "<rfc3339>",
  "spawned_from": {
    "session_id": "<uuid>",
    "node_id": "<uuid>"
  }
}
```

Emitted once at the beginning of each session. `spawned_from` is omitted for root sessions.

---

#### `node.appended`

```json
{
  "event": "node.appended",
  "node": { ... }
}
```

Emitted when a message is added to the session. This covers all cases: system/user/assistant/tool
messages, linear progression, branch continuation, and merge nodes. The `parent_ids` and
`synthesized_from` fields on the node encode the graph structure — no separate branch or merge
events are needed.

---

#### `history.rewritten`

```json
{
  "event": "history.rewritten",
  "node": {
    "id": "<uuid>",
    "session_id": "<uuid>",
    "timestamp": "<rfc3339>",
    "parent_ids": ["<canonical-parent-uuid>"],
    "synthesized_from": ["<branch-tip-uuid>", "..."],
    "message": { "role": "assistant", "content": "..." }
  }
}
```

Emitted when the harness replaces a span of history with an aggregated result. The node's
`parent_ids` point to the last canonical node before the rewrite. `synthesized_from` lists the tips
of all branches whose content was folded into the rewrite. This is a `node.appended` variant with
the rewrite semantics made explicit.

---

#### `session.ended`

```json
{
  "event": "session.ended",
  "session_id": "<uuid>",
  "timestamp": "<rfc3339>",
  "terminal_node_id": "<uuid>"
}
```

Emitted once when the session terminates. `terminal_node_id` is the HEAD at termination.

---

## Examples

### Linear Session

A two-exchange conversation. Each message is a node.

```
A[sys] → B[user] → C[asst] → D[user] → E[asst]  (HEAD)
```

Nodes:
- A: `parent_ids: []`, role `system` (root)
- B: `parent_ids: ["A"]`, role `user`
- C: `parent_ids: ["B"]`, role `assistant`
- D: `parent_ids: ["C"]`, role `user`
- E: `parent_ids: ["D"]`, role `assistant`

To resume from HEAD: collect A→B→C→D→E in order and submit as the messages array.

---

### Multi-Tool-Call Turn

A single logical "turn" where the assistant makes two tool calls before responding. Each message is
a separate node, enabling resumption from any point within the sequence.

```
A[sys] → B[user] → C[asst: tool_use(X)] → D[tool: result(X)] → E[asst: tool_use(Y)] → F[tool: result(Y)] → G[asst: "done"]  (HEAD)
```

If the container is killed after D is emitted (while waiting for inference to produce E),
resumption detects D as a dangling leaf and re-submits inference from A→B→C→D, recovering without
replaying tool call X.

---

### Fan-out / Fan-in with History Rewrite

The harness runs the session to node C, branches into two parallel explorations, then aggregates
the results and rewrites history as a single canonical node F.

```
A → B → C
         ├── branch-1: D1 → D2 → D3
         └── branch-2: E1 → E2
                  ↓
         F  (parent: C, synthesized_from: [D3, E2])
```

Nodes:
- A, B, C: linear, `parent_ids` chain normally
- D1: `parent_ids: ["C"]`
- D2: `parent_ids: ["D1"]`
- D3: `parent_ids: ["D2"]`
- E1: `parent_ids: ["C"]`
- E2: `parent_ids: ["E1"]`
- F: `parent_ids: ["C"]`, `synthesized_from: ["D3", "E2"]`

HEAD is F. To resume the session, collect F→C→B→A in reverse then assemble A→B→C→F as the messages
array. The branch nodes (D1–D3, E1–E2) are fully preserved via audit edges for observability but
are not part of the canonical context.

Events emitted (in order):

1. `node.appended` (A)
2. `node.appended` (B)
3. `node.appended` (C)
4. `node.appended` (D1)
5. `node.appended` (E1) ← concurrent with D1, timestamps distinguish ordering
6. `node.appended` (D2)
7. `node.appended` (E2)
8. `node.appended` (D3)
9. `history.rewritten` (F)

---

### Subagent Session

The parent session reaches node C (an assistant message containing a tool call that spawns a
subagent). The subagent runs its own session (X → Y → Z), and its result is returned as a tool
message in the parent session at node D.

```
Parent session:
  A → B → C[asst: tool_use(spawn)]
                    ↓
Subagent session (spawned_from: { session_id: parent, node_id: C }):
  X → Y → Z
                    ↓
  D[tool: result(subagent)] → E[asst]  (parent HEAD)
```

Sessions:
- Parent session: `{ id: "sess-parent" }`
- Subagent session: `{ id: "sess-child", spawned_from: { session_id: "sess-parent", node_id: "C" } }`

From the parent session's perspective, the subagent interaction is a tool_use message (C) and a
tool result message (D). The subagent's full graph is linked via `spawned_from` but does not appear
inline in the parent graph. The subagent session is independently resumable.

Events emitted:

1. `session.start` (sess-parent)
2. `node.appended` A, B, C (parent)
3. `session.start` (sess-child, spawned_from C)
4. `node.appended` X, Y, Z (child)
5. `session.ended` (sess-child, terminal_node_id: Z)
6. `node.appended` D, E (parent)

---

## Properties and Guarantees

| Property            | Guarantee                                                                                                                                                    |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Immutability        | Nodes are never modified after emission. History changes via new nodes only.                                                                                 |
| Append-only         | The event stream is monotonically growing. No deletions or updates.                                                                                          |
| Resumability        | Any node is a valid resumption point. Traverse `parent_ids` to root, assemble messages in order, submit as inference context.                                |
| Retroactive queries | All non-canonical history is preserved via audit edges. The full graph supports queries like "what was the session state at time T?" using node timestamps.  |
| Temporal ordering   | Wall-clock timestamps on every node enable ordering of concurrent branch nodes independent of graph topology.                                                |
| Subagent linking    | Subagent sessions reference their exact spawn point. The relationship is navigable in both directions.                                                       |
