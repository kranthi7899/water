# The tool layer

Water has two deliberately separate surfaces:

- **Interactive chat (`water`)** uses an ask-for-approval workspace profile. The directory where
  you launch Water is the only filesystem root. Roles can read and list there; every file write and
  every command pauses on a visible approval card that shows the exact target or command. `y` allows
  that one action; `n` denies it. The scope disappears when the chat ends. It does not grant access
  to the rest of your home directory, Water's own state directory, the network, or another role's
  memory.
- **Orchestration and headless `water run`** remain deny-by-default. `tools.enabled` is false by
  default; CTO and Design can only read roots explicitly named in configuration, and CEO/COO retain
  no ambient machine access. This prevents a background graph from unexpectedly pausing for a
  terminal approval or inheriting the directory where Water happened to start.

## Enabling

    water config set tools.enabled true
    water config set tools.roots "~/work/board-materials,~/work/specs"

Roots are explicit and absolute (or `~`-relative) for headless and orchestration runs. In chat, the
launch directory is the one explicit workspace root, shown in `/status`.

## What happens on a call

1. The role's subprocess (`claude -p`) is started with `--tools ""` (no built-in tools),
   `--strict-mcp-config` (none of your connectors) and `--mcp-config` pointing at
   `water mcp-serve --policy <0600 file> --log <jsonl>`.
2. The model requests a tool over MCP. Water resolves the path against the declared roots —
   symlinks and `..` included — and decides. In interactive chat, a write or command travels on a
   private local socket to the TUI, which shows an approval card and returns one correlated decision.
   Denials are returned to the model as text and traced.
3. Every invocation lands in the run trace: role, tool, arguments, decision, basis, result hash,
   size, a 2 KiB preview, duration. Full results are not stored (they may be sensitive).
4. Content returned to the model is wrapped in UNTRUSTED markers, and the node that consumed it is
   marked so that every message it emits carries `untrusted: true`. Downstream prompts render that
   label.

## Guarantees, honestly

- Permission checks constrain what Water executes, not what an executed process does.
- Interactive chat shell commands require approval every time and, on macOS, run inside a
  `sandbox-exec` profile (deny default, workspace-only filesystem access, no network). Linux has no
  kernel confinement in this build, so it refuses chat shell execution; Landlock is the planned fit.
  Windows: refused for the same reason.
- Containers are opt-in only and not part of the install path.
- Codex: MCP tools are not wired (could not be verified without the CLI); roles get no tools there.
