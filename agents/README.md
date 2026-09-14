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

Phase 1 ships these blank. Persona prose is Phase 2 work with a different quality bar.
