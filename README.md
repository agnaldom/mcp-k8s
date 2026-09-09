# mcp-k8s

A **read-only** [MCP](https://modelcontextprotocol.io) server that provides **structured Kubernetes facts** — with limits, sanitization, and an audit trail.

It does not reason, correlate causes, or recommend actions. Every field it returns is either read directly from the Kubernetes API or derived from it by a declared, deterministic rule. If it isn't, it doesn't get returned.

- **Language:** Go
- **Transport:** stdio (v0.1)
- **Cluster access:** kubeconfig contexts or in-cluster (pod ServiceAccount)
- **Mutations:** none. There is no hidden `--allow-write` flag. Writes, when they come, will be a separate binary with a separate ServiceAccount and separate RBAC — a config bug must never turn a read-only server into an admin.

## Why not just kubectl or a shell?

Because an AI agent consuming raw Kubernetes objects gets two failure modes this server is designed to remove:

1. **Silence is ambiguous.** kube-apiserver's `--event-ttl` (default 1h) means that in a two-hour-old incident, events from before the rollout simply don't exist. Without a declared window, "no failures before the rollout" and "no data about before the rollout" both arrive as an empty list — the older the incident, the more often an agent confirms its own hypothesis. Every time-bound tool in `mcp-k8s` declares the window it provably covers via `coverage`:

   ```json
   "coverage": {
     "from": "2026-09-09T14:58:00Z",
     "to":   "2026-09-09T15:31:00Z",
     "complete": false,
     "reason": "oldest available event is newer than requested window (likely --event-ttl)"
   }
   ```

   `complete: false` forces the consumer to treat absence as *unknown*, not as *no failures*.

2. **Judgment disguised as data.** A signal like `"high_restart_count": true` is opinion. `mcp-k8s` signals ship the rule that fired and the observed value, so the consumer can disagree with the threshold — because the threshold is in the response:

   ```json
   {
     "type": "high_restart_count",
     "severity": "warning",
     "rule": { "expression": "restartCount >= 10", "window": "1h", "configurable": true },
     "observed": { "restartCount": 17 },
     "source": "pod/payments-api-89abc .status.containerStatuses[api].restartCount"
   }
   ```

## Tools

Twelve tools, all read-only, all requiring an explicit `cluster` argument — there is no implicit "current context" and no mutable global state, so one session can never silently switch another session's cluster.

| Tool | What it returns |
|---|---|
| `k8s_cluster_list` | Available clusters/contexts, with the default flagged |
| `k8s_cluster_info` | Kubernetes version and detected capabilities |
| `k8s_namespace_list` | Namespaces after policy filtering |
| `k8s_api_resources` | API discovery **with effective permissions** (`SelfSubjectAccessReview`), not just cluster capabilities |
| `k8s_resource_list` | Any Kind, via dynamic client, with pagination and `view` projection |
| `k8s_resource_get` | Any Kind, with `view` projection |
| `k8s_pod_logs` | Container logs, including `previous` (essential for CrashLoopBackOff/OOMKilled) |
| `k8s_events_list` | Normalized events, each carrying `coverage` |
| `k8s_metrics_pods` | Resource consumption per pod |
| `k8s_metrics_nodes` | Resource consumption per node |
| **`k8s_workload_context`** | The troubleshooting bundle in one call: workload state, pods, owner/Services/EndpointSlices/HPA/PDB/PVC relationships, events with coverage, metrics, signals |
| **`k8s_signals`** | Twelve deterministic signals with declared thresholds (restart count, OOMKilled, CrashLoopBackOff, image pull errors, pending PVCs, unschedulable/evicted pods, memory near limit, and more) |

Two tools that deliberately **don't** exist: `k8s_context_list`/`k8s_context_current` (a kubeconfig context *is* a cluster) and `k8s_resource_describe` (~80% overlapped with `k8s_workload_context`).

### Response envelope

```json
{
  "apiVersion": "mcp-k8s/v1",
  "cluster": { "id": "...", "name": "prod" },
  "data": {},
  "coverage": { "from": "...", "to": "...", "complete": true },
  "meta": { "timestamp": "...", "durationMs": 81, "view": "summary" }
}
```

### Views and limits

- `view: summary` (default) — identity, state, and what changes during an incident. `view: full` — the complete normalized object, still sanitized.
- Always removed in both views: `metadata.managedFields`, the `last-applied-configuration` annotation, and annotations over 4 KiB.
- Lists page normally (`pagination.continue`); logs truncate at the **beginning** and report `meta.droppedLines`.
- A single object larger than the response cap is an error (`RESPONSE_TOO_LARGE`), never a silently cut JSON.
- Defaults that matter: 500 log lines (max 5000 / 1 MiB), 100 items per list (max 500), 20 detailed pods per `workload_context`, max 8 concurrent Kubernetes requests per call, 30s tool timeout with cascading sub-timeouts.

## Security model

**Deterministic controls** (tested by merge-blocking regression tests):

- `Secret` is blocked by policy even when RBAC would allow it — `Secret.data`/`stringData` are never serialized.
- `env[].value` is redacted as `[REDACTED]`; `env[].valueFrom.secretKeyRef` returns the secret/key names but never resolves the value.
- kubeconfig, tokens, and certificates never appear in responses or logs; exec credential plugins are disabled by default (`allowExecPlugins: false`).
- The audit log records tool, cluster, namespace, resource, result, duration, and bytes out — never content, tokens, or credentials.

**Defense in depth (explicitly not a control):** secret redaction inside log *content* (Bearer tokens, JWTs, PEM keys, connection strings) is best-effort regex over arbitrary application text. The real control is RBAC — don't grant `pods/log` broadly. Redaction reduces damage when a log was already read; it does not authorize reading it.

## Configuration

`mcp-k8s` reads a YAML config file (e.g. `~/.config/mcp-k8s/config.yaml`):

```yaml
security:
  readonly: true
  clusters:   { allow: [] }                    # empty = all contexts allowed
  namespaces: { allow: [], deny: [kube-system, cattle-system] }
  resources:  { deny: [Secret, TokenRequest] }
  kubeconfig: { allowExecPlugins: false }

limits:
  requestTimeout: 30s
  response:  { maxBytes: 4194304 }             # 4 MiB
  list:      { defaultLimit: 100, maxLimit: 500 }
  logs:      { defaultTailLines: 500, maxTailLines: 5000, maxBytes: 1048576 }
  events:    { maxItems: 500 }
  workload:  { maxPods: 20, maxConcurrentK8sRequests: 8 }

kubernetes:
  qps: 20
  burst: 40

signals:                                     # override configurable thresholds
  container_not_ready: { notReadyFor: 5m }
  high_restart_count:  { threshold: 10, window: 1h }
  pvc_pending:         { pendingFor: 2m }
  memory_near_limit:   { ratio: 0.90 }
```

Validate it without touching a cluster:

```bash
mcp-k8s config validate
```

## CLI

```bash
mcp-k8s serve --transport stdio   # run the MCP server
mcp-k8s doctor                    # config, connectivity, discovery, metrics, permissions, cache
mcp-k8s config validate           # validate config file
mcp-k8s cluster list              # list clusters/contexts
mcp-k8s cluster test <name>       # test connectivity to a cluster
mcp-k8s version
```

`doctor` is the first thing to run when something doesn't work.

## Setup with AI tools

`mcp-k8s` speaks MCP over stdio, so any MCP-capable client can launch it as a subprocess. The universal shape is:

```json
{
  "mcpServers": {
    "mcp-k8s": {
      "command": "mcp-k8s",
      "args": ["serve", "--transport", "stdio"]
    }
  }
}
```

### Claude Code

```bash
claude mcp add mcp-k8s -- mcp-k8s serve --transport stdio
```

Or add it to the project-scoped `.mcp.json` (commit it to share with your team):

```json
{
  "mcpServers": {
    "mcp-k8s": {
      "command": "mcp-k8s",
      "args": ["serve", "--transport", "stdio"]
    }
  }
}
```

### Codex

Add to `~/.codex/config.toml` (or a project `config.toml`):

```toml
[mcp_servers.mcp-k8s]
command = "mcp-k8s"
args = ["serve", "--transport", "stdio"]
```

### Kimi Code

Register the server in Kimi Code's MCP configuration (same stdio JSON shape):

```json
{
  "mcpServers": {
    "mcp-k8s": {
      "command": "mcp-k8s",
      "args": ["serve", "--transport", "stdio"]
    }
  }
}
```

### GitHub Copilot (VS Code)

Add to your user or workspace `.vscode/mcp.json`:

```json
{
  "servers": {
    "mcp-k8s": {
      "command": "mcp-k8s",
      "args": ["serve", "--transport", "stdio"]
    }
  }
}
```

### Other MCP clients (Cursor, Zed, etc.)

Any client that supports local stdio MCP servers accepts the same universal JSON block under its MCP-servers setting, with `command` pointing to the `mcp-k8s` binary.

## Deployment notes

- **RBAC:** grant a minimal read-only permission set; never grant `pods/log` broadly — that is the real log-secrecy control.
- **Cache:** discovery is cached on disk at `~/.cache/mcp-k8s/` (0700/0600) so clients that spawn the server per command get instant subsequent calls.
- **In-cluster:** run with a dedicated ServiceAccount, mount the RBAC manifests, and set the in-cluster provider in config.

## Roadmap

| Version | Content |
|---|---|
| v0.1 | This server: read-only, stdio, kubeconfig + in-cluster |
| v0.2 | Rancher provider · streamable HTTP transport · `watch` |
| v0.3 | Persistent event collector (solves `--event-ttl`) · more signals |
| v0.4 | `mcp-k8s-admin` — separate write binary with proposal, dry-run and approval |

## Repository rules

1. No company, client, or internal-project names in code, directories, comments, tests, fixtures, or commit messages. Use `example.com`, `payments`, `prod`.
2. No `exec.Command("kubectl", ...)`.
3. No LLM SDK in this repository.
4. `internal/kubernetes` never imports `internal/tools` — the dependency direction is `tools → services → kubernetes`.

## License

See [LICENSE](./LICENSE).
