# The tool layer

Water's roles reason; they do not run tools on your machine unless you say so. The default is
deny: `tools.enabled` is false, no role has shell or network, and the two roles that can read
files (CTO, Design) can read nothing until you name roots.

## Enabling

    water config set tools.enabled true
    water config set tools.roots "~/work/board-materials,~/work/specs"

Roots are explicit and absolute (or `~`-relative). The working directory is never a root.

## What happens on a call

1. The role's subprocess (`claude -p`) is started with `--tools ""` (no built-in tools),
   `--strict-mcp-config` (none of your connectors) and `--mcp-config` pointing at
   `water mcp-serve --policy <0600 file> --log <jsonl>`.
2. The model requests a tool over MCP. Water resolves the path against the declared roots —
   symlinks and `..` included — and decides. Denials are returned to the model as text and traced.
3. Every invocation lands in the run trace: role, tool, arguments, decision, basis, result hash,
   size, a 2 KiB preview, duration. Full results are not stored (they may be sensitive).
4. Content returned to the model is wrapped in UNTRUSTED markers, and the node that consumed it is
   marked so that every message it emits carries `untrusted: true`. Downstream prompts render that
   label.

## Guarantees, honestly

- Permission checks constrain what Water executes, not what an executed process does.
- No role has shell in v1. The `run` tool exists behind an allowlist and, on macOS, a
  `sandbox-exec` profile (deny default, read roots only, no network). Linux has no kernel
  confinement in this build; Landlock is the planned fit. Windows: none.
- Containers are opt-in only and not part of the install path.
- Codex: MCP tools are not wired (could not be verified without the CLI); roles get no tools there.
