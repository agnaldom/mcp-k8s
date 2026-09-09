# AGENTS.md — Working rules for this repository

Guidance for AI agents and contributors working in this repository. The source of
truth for the product is [`spec.md`](./spec.md) — read it before making
non-trivial changes. These rules are permanent (spec §15); do not relax them
without an explicit decision recorded in the spec.

## Permanent rules (spec §15)

1. **No company, client, internal-project, or corporate-domain names** anywhere
   in code, directories, comments, tests, fixtures, or commit messages. Use
   `example.com`, `payments`, `prod`.
2. **No `exec.Command("kubectl", ...)`** — ever. All cluster access goes through
   client-go.
3. **No LLM SDK** in this repository. This is a facts provider, not an agent.
4. **Layering:** `internal/kubernetes` must not import anything from
   `internal/tools`. Dependency direction is
   `tools → services → kubernetes` — the Kubernetes layer must work without an
   MCP server.

## Project shape

- Read-only MCP server over stdio. **No mutations, no hidden `--allow-write`.**
- Every tool call takes an explicit `cluster` argument; there is no mutable
  global context state.
- Tool names: underscore-separated, `k8s_` prefix (spec §3.1).
- Every response uses the `mcp-k8s/v1` envelope with `cluster`, `data`,
  `coverage`, `meta` (spec §3.2); time-bound data always declares `coverage`.
- Errors use the catalog in spec §3.6.

## Language and communication

- **All project artifacts in English:** code, comments, commit messages,
  GitHub issues, README, docs, tests.
- Commit messages: imperative, short, no company references.

## How work is organized

- The implementation plan lives in **spec §13** and is tracked as GitHub
  issues #1–#23 (one per step).
- **Workflow for every development task:**
  1. **Read the open issues first** (`gh issue list`) and pick the issue(s)
     to work on. Never start a step without its issue.
  2. **Open a new branch** named after the issue, e.g.
     `git checkout -b 02-cobra-cli-config-logging main`.
  3. Implement and verify (`make ci`).
  4. **Open a PR** that references and closes the issue
     (`Closes #N` in the PR body) and get it merged.
  5. Close the issue when the PR is merged and the step is verified.
- Follow spec §13 roughly in order; steps #7 onward are individually usable as
  soon as they land.
- **Security controls are merge-blocking.** Changes touching sanitization,
  policy, or limits must keep the regression scenarios in spec §11 (and issue
  #21) passing.

## Verification before declaring anything done

- `make build` and `make test` must pass on the changed code.
- New behavior in `internal/kubernetes` is covered by unit tests with fakes
  (`fake.Clientset`, dynamic fake, fake discovery); integration behavior uses
  the kind fixtures from spec §11.
- Never claim a security guarantee that spec §6.2 does not claim — log-content
  redaction is defense in depth, not a control.

## Spec drift

If the code and `spec.md` disagree, the spec wins until someone deliberately
changes the spec. Update `spec.md` in the same PR when the contract changes.
