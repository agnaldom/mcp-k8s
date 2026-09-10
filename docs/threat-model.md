# Threat model

This document states what `mcp-k8s` defends against, what it does not,
and where the real boundaries are. It exists so that no one deploys the
server with a false sense of what it guarantees (spec §6.2).

## Assets

- **Cluster credentials.** Kubeconfig contents, bearer tokens, client
  certificates, and exec-plugin outputs. They are resolved per call and
  never serialized into responses or logs.
- **Workload data.** Resource manifests, events, metrics, and logs that
  an LLM agent reads on behalf of a user.
- **The host running the server.** A compromised server must not become
  a pivot into the cluster with more rights than intended.

## Trust boundaries

1. **LLM client → server.** Prompts are untrusted input. The server
   never executes prompt-derived strings: there is no
   `exec.Command("kubectl", ...)` and no shell anywhere in the code
   path, and the distroless image contains no shell to find.
2. **Server → cluster API.** All traffic is client-go over TLS. The
   credential that reaches the API is always the one belonging to the
   cluster named in the request — concurrent requests for different
   clusters never share state (proven by the merge-blocking isolation
   test, spec §11).
3. **Server → local disk.** Only the discovery cache
   (`~/.cache/mcp-k8s/`, mode 0700/0600) and the config file are
   touched. Credentials are never written to disk by the server.

## Controls (deterministic, merge-blocking regression tests)

| Threat | Control |
|---|---|
| Agent reads a Secret | Kind blocked by local policy before any cluster call; `Secret.data`/`stringData` never serialized even if policy is misconfigured |
| Secret value via env | Literal `env[].value` → `[REDACTED]`; `secretKeyRef` returns names only, never resolves values |
| Metadata bloat / history leak | `managedFields` and `last-applied-configuration` always stripped |
| Credential exfiltration via response or log | Kubeconfig, tokens, certs never serialized; exec credential plugins disabled by default (`allowExecPlugins: false`) |
| Oversized responses | Per-object byte cap returns `SizeError`; log byte budget enforced with truncation reported |
| Cross-cluster credential mixing | Stateless provider/factory; no global mutable context; concurrency isolation test |

## Defense in depth — NOT a control

Secret redaction inside **log content** (Bearer tokens, JWTs, PEM
private keys, connection strings, cloud keys) is **defense in depth,
not a control**.

Application logs are arbitrary text. Regex redaction over arbitrary
text has a high, unmeasurable false-negative rate. Calling it a control
produces false confidence and does not survive a serious audit.

**The real control is RBAC: do not grant `pods/log` broadly.** The
shipped ClusterRole (`deploy/rbac.yaml`) deliberately omits `pods/log`;
grant it per-namespace with an additional RoleBinding only where an
operator accepts the risk. Redaction reduces the damage when a log was
already read — it does not authorize reading it.

The same honesty applies to response views and caps: they bound size,
they do not sanitize intent. Anything that looks like a secret and is
not covered by the deterministic table above must be assumed readable
by whoever can read the resource.

## Read-only scope

The binary has no hidden write path and no `--allow-write` flag (spec
§2). Writes are planned as a separate binary (`mcp-k8s-admin`) with its
own ServiceAccount, its own RBAC, and proposal/dry-run/approval flow.
Until then, the worst a fully compromised server can do to the cluster
is read what its ServiceAccount can read — which is why the RBAC
manifest is the second most important artifact in this repository,
after the test suite.
