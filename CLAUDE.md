# Water

**Read `docs/CONTEXT.md` first.** Water is being rebuilt from a four-role council into a single CEO digital-twin runtime. `docs/EVOLUTION_PLAN.md` holds the slice order, the current status and what comes next. Work happens on branch `feat/ceo-twin`.

## Rules for every session
- **Zero metered spend.** Every model call goes through the Claude subscription via the `claude` CLI. Never set `allow_metered`, never make the API backend the default, and never add a paid STT/TTS/SMS/voice service. Tests use `backend.Fake`.
- **Build one slice per session.** Don't start the next slice. When a slice ends, update `docs/EVOLUTION_PLAN.md` with what changed and what's next.
- **Gates before every commit:** `go vet ./...`, `go test -count=1 ./...` and `CGO_ENABLED=0 go build ./cmd/water` must all pass. Commit at the end of each phase.
- **Dependencies:** the approved new ones are `modernc.org/sqlite` and `github.com/modelcontextprotocol/go-sdk`. Ask before adding anything else, and never add a cgo dependency.
- **Outward actions:** nothing may send, post, call, delete or spend without a passing gate check and an approved envelope.
- **Build and embed:** Go is at `/opt/homebrew/bin/go` on the Mac. `agents/` and `themes/` are go:embed'd, so rebuild after re-stamping persona files.
