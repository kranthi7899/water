# agents/

Persona content, embedded into the binary at build time (`//go:embed all:agents` in `embed.go`).

Each subdirectory with a valid `role.yaml` is a role. Adding a role is adding a folder (and,
optionally, `themes/<slug>.yaml` for its look).

    agents/<slug>/
      role.yaml            manifest: role_id, capability manifest, backend/model, tools grant
      soul.md              always loaded — identity, mission framing, place in the run
      experience.md        always loaded — distilled first-person prose (uncited)
      .index.json          hidden sentence→source-record index for experience.md
      skills/<name>/SKILL.md   Anthropic-schema skills, loaded by description match
      memory/session.md    seed for the per-role bounded memory (copied to ~/.water/memory)

Every persona file carries `role_id`, `file_type` and `content_hash` in its frontmatter and is
verified against its folder's `role.yaml` at load. Edit with `water persona edit` (which re-stamps
and journals) or run `water persona sign --no-signature` after a manual edit.
