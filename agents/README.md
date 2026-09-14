# agents/

Persona content, embedded into the binary at build time (`//go:embed all:agents` in `embed.go`).

Each subdirectory with a valid `role.yaml` is a role. Adding a role is adding a folder.

    agents/<slug>/
      role.yaml            manifest (schema 1)
      soul.md              always loaded — identity, mission framing
      experience.md        always loaded — distilled first-person prose (uncited)
      .index.json          hidden sentence→source-record index for experience.md
      skills/<slug>/SKILL.md   contextually loaded reasoning procedures
      memory/session.md    seed for the per-role bounded memory (copied to ~/.water/memory on first use)

Phase 1 shipped these blank. Phase 2 (2026-09-14) wrote soul.md/experience.md/.index.json for all
four roles (ceo, coo, cto, design), adapted from verified persona-research content in a sibling
project (`~/twin`).
