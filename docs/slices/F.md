# Slice F: memory store (structured records, provenance, supersedes, never-store enforcement)

**Status: approved by the owner 2026-09-25 and implemented (uncommitted at
the time of writing). The decisions made at Approve are recorded under
"Owner decisions" at the end.** This is the first sub-slice of
the intelligence-layer redesign (F through Q). The overview and sequencing
doc for that redesign lives alongside these specs and holds the full
open-questions list. This file is the spec to build F against once the owner
approves it, following `docs/WORKFLOW.md`'s Explore → Plan → Approve →
Implement loop. It is foundational: G (memory access paths) and Q
(procedural memory) build directly on it, and H, I, O and P build on G.

Builds on A1–A4 and nothing later: `audit` (the hash-chained log whose
`audit.Entry.Seq` a record's provenance cites), `vault` (Keychain-backed
`(service, account)` secrets, the only acceptable home for payment details),
and `store` (SQLite, migrations `0001`–`0013`). Before F, Water has a
`internal/memory` package that nothing uses. F turns it into a real,
structured, append-only fact store with tests. It does **not** connect it to
the daemon, the system prompt, the model's tools, config or the CLI. All of
that is G.

## Principle

Memory holds **distilled facts with provenance, never the source content
itself**. A record says "the CEO prefers board updates on the first Monday"
and cites where that came from (an audit sequence number and a source ref
such as `gmail:msg-abc123`). It never holds the email that said so. A
correction **invalidates** the old record and links the new one to it. It
never edits or deletes the old one, so "what did the twin believe on
2026-10-03, and why?" always has an answer.

`docs/CONTEXT.md` principle 6 is already law and F enforces it in code:
long-term memory is written *only* from explicit CEO statements and approved
corrections, each with provenance.

## What exists today (read in full, 2026-09-25)

- `internal/memory/memory.go` and `markdown.go` are council-era: "bounded,
  curated, per-role memory" on the Hermes pattern.
- `Entry` is only `{ID, Text, CreatedAt, Tags}`. `Provider` exposes
  `Snapshot/Add/Replace/Remove` keyed by **role**. `Scoped`/`Bind` give a
  node a single-role handle with no method that takes a role argument, so
  isolation is a property of the API surface, not a convention. There is also
  a named-provider registry (`Register/Open/Names`) and `Limits` (default 200
  entries / 32 KiB) that fail loudly with `ErrBoundsExceeded`. That error's
  message still tells the user to run `water memory <role> prune`, a command
  deleted in A2.
- The single provider, `"markdown"`, writes `Root/<role>/session.md` with
  `## [id] RFC3339 #tags` headers, a per-role file lock
  (`lock_unix.go`/`lock_other.go`) and an atomic tmp+rename. Bounds are
  checked on read as well, so a hand-edited file that grew past its limits
  fails to load.
- `Replace` overwrites in place and `Remove` deletes. The package has no
  supersedes link, provenance, time bounds, type, sensitivity, search or
  never-store check.
- No package outside `internal/memory` imports it (grep-confirmed). It is
  fully unwired.
- `internal/config` lists `"memory."` in `retiredPrefixes`, so any
  `memory.*` key is silently ignored today. F leaves that untouched, and G
  undoes it deliberately.

## Scope

### 1. The record

```go
// internal/memory/record.go
type Type string
const (
    Preference  Type = "preference"
    Pattern     Type = "pattern"
    Entity      Type = "entity"
    Procedure   Type = "procedure"
    Commitment  Type = "commitment"
    Decision    Type = "decision"
    ContextNote Type = "context_note"
)

type Record struct {
    ID         string      // "mem_" + crypto/rand hex, same idiom as approvals.newID()
    Type       Type        // closed set above; anything else is rejected
    Statement  string      // the distilled fact, in one or a few sentences
    Sensitivity Sensitivity // see "Sensitivity" below; required
    Subjects   []string    // optional typed refs for search, e.g. "person:<roster id>",
                           // "project:<id>"; refs only, never names-plus-content
    Provenance Provenance
    Time       TimeBounds
    Confidence float64     // 0..1, informational like decisions.Classification.Confidence;
                           // never itself a gate in F
    Supersedes string      // id of the record this one corrects, or ""
    Invalidation *Invalidation // nil while live; set exactly once, never cleared
}

type Provenance struct {
    Trigger    Trigger // ceo_statement | approved_correction | approved_proposal
    SourceRef  string  // a citation, e.g. "gmail:msg-abc123", "audit:1842",
                       // "turn:<id>"; must be a reference, never content
    AuditSeq   int64   // the audit.Entry.Seq of the event that produced this write
    WrittenBy  string  // who produced the text: "ceo" or "twin"
    ApprovedBy string  // who approved it; required unless Trigger is ceo_statement
}

type TimeBounds struct {
    ObservedAt  time.Time  // required: when the fact was observed or stated
    ValidFrom   time.Time  // defaults to ObservedAt
    ValidUntil  *time.Time // nil = open-ended
    ReviewAfter *time.Time // nil = no scheduled review
}

type Invalidation struct {
    At       time.Time
    By       string // "ceo", or the approver
    Reason   string
    AuditSeq int64
    SupersededBy string // "" when invalidated without a replacement
}
```

**Validation is a plain Go `Validate()` method** on `Record`, consistent with
`twins.Manifest` and `decisions.LoadRegistry`. It uses no schema library. It
rejects:

- an unknown `Type` or `Sensitivity`
- an empty `Statement`
- a `Provenance` with an empty `SourceRef`, `AuditSeq <= 0`, or an empty
  `WrittenBy`
- an empty `ApprovedBy` on any trigger other than `ceo_statement`. This is
  principle 6 in code: a twin-written record must name an approver.
- a zero `ObservedAt`, `ValidUntil <= ValidFrom`, or `ReviewAfter` before
  `ValidFrom`
- `Confidence` outside `[0, 1]`
- a `Supersedes` id that does not exist, or that points at a record that is
  already invalidated (you correct the current belief, not a dead one)

F checks that `AuditSeq` is present and positive. It **cannot** check that
the sequence number is real, because F has no audit wiring. G binds writes to
a live `audit.Log`.

**Sensitivity.** The brief requires a sensitivity field but does not define
its levels. F stores a value from a small closed set defined in exactly one
place (`sensitivity.go`) and rejects anything else. The owner decides the
actual tiers. At Approve (2026-09-25) the owner chose to ship the
**provisional** placeholder set (`normal | sensitive | restricted`),
labelled provisional in the code comment, until the real tiers are named. F gives the levels no behavior of
their own. Filtering by sensitivity is only a query parameter.

### 2. Append-only writes: invalidate, never overwrite

Records are immutable once written. The only mutation that exists is setting
`Invalidation` once, and an invalidation can never be cleared. The new API
has no `Replace` and no `Remove`.

```go
// internal/memory/store.go
type Store interface {
    // Write validates r (including the never-store check, §3) and appends it.
    Write(ctx context.Context, r Record) (Record, error)
    // Supersede writes next with next.Supersedes = oldID and invalidates oldID
    // with SupersededBy = next.ID, atomically: both happen or neither does.
    Supersede(ctx context.Context, oldID string, next Record, inv Invalidation) (Record, error)
    // Invalidate retires a record with no replacement (the fact is simply no
    // longer true). Invalidating an already-invalidated record is an error.
    Invalidate(ctx context.Context, id string, inv Invalidation) error

    Get(ctx context.Context, id string) (Record, error)                   // any record, live or not
    Current(ctx context.Context, now time.Time, q Query) ([]Record, error) // live records only
    Search(ctx context.Context, now time.Time, q Query) ([]Record, error)
    ListByType(ctx context.Context, t Type, now time.Time, includeHistory bool) ([]Record, error)
    DueForReview(ctx context.Context, now time.Time) ([]Record, error)
    History(ctx context.Context, id string) ([]Record, error) // the supersedes chain, oldest first
}
```

**"Current" is defined in one pure function** (`IsCurrent(r, now)`), not
per backend. A record is current when `Invalidation == nil`,
`ValidFrom <= now`, and `ValidUntil` is nil or later than `now`. Superseded
records always carry an `Invalidation` (Supersede sets it atomically), so
"not superseded" needs no separate check. `ReviewAfter` never hides a record.
It only puts the record on `DueForReview`.

**Search** is deterministic: a case-insensitive term match over `Statement`
and `Subjects`, filtered by `Query{Types, Subjects, Sensitivities,
IncludeHistory, Limit}`, ordered by `ObservedAt` descending and then by `ID`.
It has no embeddings, no model call and no relevance model. Ranking beyond
this is a later concern and is not needed by G, Q or anything else in the
redesign.

**Scoping.** The council-era API scoped memory by *role*, and there is now
exactly one role. F keeps the isolation property but changes the key.
Memory is scoped by **twin id** (`ceo`, `ceo-demo`, `counterparty`). A
`Bind(store, twinID)` handle is the only thing a caller receives, and it has
no method that takes a twin id. This is the same "property of the API
surface, not a convention" guarantee `Scoped` gave, and it stops the demo
twin or the counterparty ever reading the CEO's memory.

**Bounds** carry over. `Limits` and `ErrBoundsExceeded` stay, counted over
*live* records, and the error text no longer names the deleted `prune`
command. F does not decide what happens to invalidated history as it grows
(keep forever, archive, or compact). The owner decided at Approve: keep forever for now (see "Owner decisions").

**The council-era surface is retired.** `Entry`, the role-keyed `Provider`,
`Scoped`, `Prune`, `Sizer` and the `session.md` format are removed in this
slice. Nothing imports them (grep-confirmed), and keeping two memory shapes
side by side would invite someone to wire the wrong one. The named-provider
registry (`Register/Open/Names`) stays if the owner picks more than one
backend, and is dropped otherwise. If the owner wants the old files migrated
instead, confirm it at Approve. There are no live `session.md` files under
`~/.water` from the single-twin era to migrate, because nothing has written
one since A2. The implementing session must verify this on the real machine
before deleting anything.

### 3. The never-store validator, enforced in code at write time

The brief's list, verbatim: **raw email/message bodies, raw conversation
transcripts, credentials/tokens, payment details beyond a vault handle,
government ID numbers.**

`CheckNeverStore(r Record) error` runs inside every `Write` and `Supersede`,
at the package level, *before* any backend sees the record. A backend cannot
be handed an unchecked record, and no write path skips the check. It also
runs **on read**, the same way `CheckBounds` runs on read today: a
hand-edited or corrupted store holding a forbidden value fails to load
loudly rather than serving it. The check covers `Statement`, `Subjects` and
every `Provenance` string field. A rejection returns a typed
`ErrNeverStore{Category, Field}` and **never echoes the offending text** in
the error message, since a logged error would otherwise defeat the rule.

Detection per category:

| Category | Check (all in code, no model call) |
|---|---|
| Credentials / tokens | Known secret shapes: PEM `-----BEGIN ... KEY-----` blocks, `Bearer ` headers, JWT three-segment base64url shape, common provider prefixes (Google `ya29.`/`1//`, `sk-`, `ghp_`/`gho_`, `xox[abpr]-`, `AKIA`), plus long high-entropy tokens above a length threshold. |
| Payment details beyond a vault handle | Luhn-valid 13–19 digit runs (spaces and dashes allowed), IBAN shape with a valid mod-97 checksum, and CVV/expiry next to a card-like number. A payment instrument may only appear as a vault handle, `vault:<service>/<account>`, which matches `vault.Vault`'s real `(service, account)` addressing and is checked for that exact form. |
| Government ID numbers | US SSN shape (`ddd-dd-dddd` and the 9-digit form, with SSA's invalid ranges excluded to cut false positives). Other jurisdictions are deferred (owner decision at Approve, provisional; see "Owner decisions"). |
| Raw email/message bodies | Structural signals that the text is a copied message rather than a distilled fact: RFC 822 header lines (`From:`, `To:`, `Subject:`, `Date:`, `Message-ID:`), quoted-reply markers (lines starting `>`, "On … wrote:"), signature delimiters (`-- ` line), and a hard `Statement` length cap. |
| Raw conversation transcripts | Multi-speaker turn shape (two or more lines of `Name: …` / `Speaker N:` / timestamped `[hh:mm]` turns), `User:`/`Assistant:` role markers, and the same length cap. |

**This is one of two enforcements, not the only one** (owner amendment,
2026-09-25). F's validator is the code-level backstop at the write path.
The other is the boundary stated up front in the agent's own operating
context (`role.md` and the core-memory block) as a standing principle,
the same way its responsibilities are stated. That second layer reaches
the model, so it is G's (G §3a). F builds only the backstop. Neither layer
replaces the other.

Two rules keep this honest:

- **The validator is a floor, not a guarantee.** Pattern detection has false
  negatives (a paraphrased email body will pass) and false positives (a
  long digit run in a legitimate figure might look card-shaped). The spec
  accepts this, and the tests pin down behavior on a fixture corpus in both
  directions. The structural defense is §2's shape: a record is a short
  statement plus a *reference* to its source, so there is no field meant to
  hold content. `SourceRef` must parse as `<scheme>:<id>` with no whitespace
  and a short length cap. A body pasted into it fails on shape alone.
- **The thresholds are provisional.** The `Statement` length cap and the
  entropy/length threshold for tokens are set in one place, listed as owner
  decisions below, and not presented as calibrated.

### 4. Storage backend: an owner decision, not assumed

Both options satisfy the same `Store` interface and the same contract test
suite (§Tests). The owner picks one at Approve, and F builds only that one.

- **Extended markdown** (one file per twin, `~/.water/memory/<twin>/memory.md`,
  keeping today's lock + atomic tmp+rename). This matches CONTEXT.md
  principle 6's wording ("markdown files") and stays human-readable and
  git-friendly. The cost is that each record needs a structured header
  (front-matter-style fields per entry, not today's one-line
  `## [id] time #tags`), search is a linear scan (fine at the bounded sizes
  F keeps), and Supersede's atomicity comes from rewriting one file under the
  lock.
- **SQLite migration** (a new `memory_records` table in the existing
  database, one row per record, with indexes on `(twin, type)`,
  `valid_until`, `review_after` and `supersedes`, and optionally an FTS5
  index as Slice M's local index already uses). This gives real indexes and
  transactional Supersede, and it sits next to the records G and Q will join
  against. The cost is that it departs from principle 6's "markdown files",
  so choosing it means amending `docs/CONTEXT.md` in the same slice. The
  migration number is **not** reserved here. `0014` is the next free number
  today, but the number is chosen at commit time, because concurrent
  sessions collided on migration numbers twice during Slice R.

## Do not build in F

- **Anything that reaches the running daemon.** That means the core-memory
  block in `runtime.RoleSystem`, the `memory.search`/`propose`/`invalidate`
  tools, a connector registration or `twin.yaml` grants, re-admitting
  `memory.*` config keys, the replacement `water memory` CLI, and binding
  `AuditSeq` to the live audit log. All of that is **G**.
- **Model-proposed records and their pending/approval area.** F accepts only
  records whose provenance already satisfies principle 6. Where unapproved
  proposals wait, and who approves them at what gate level, is G's design.
- **When records get written.** F is a store with no write cadence of its
  own. The three write triggers fixed by the owner's amendment
  (2026-09-25) are G's (G §3b) and Q's: an explicit CEO statement written
  live, a pattern only through a periodic consolidation pass, and a
  procedure only once its task is terminal. None of them is "at session
  end", and F adds no hook that assumes one.
- **Procedural-memory matching or promotion.** F stores `procedure` records
  like any other type. Retrieving one "by shape" and promoting completed
  tasks into procedures is **Q**.
- Embeddings, semantic search, relevance ranking, automatic decay or
  confidence-based filtering.

## Tests

The implementing session runs `go vet ./...`, `go test -count=1 ./...` and
`CGO_ENABLED=0 go build ./cmd/water` before every commit, as always. This
planning doc runs none of them.

- **Contract suite.** One table-driven suite (`storetest`) that takes a
  `Store` constructor and runs everything below against it. The chosen
  backend runs it. A second backend added later must run it unchanged.
- **Record validation.** Every rejection rule in §1 has a failing fixture,
  and a well-formed record of each of the seven types round-trips exactly
  (field-for-field equality after `Get`).
- **Principle 6.** A `twin`-written record with an empty `ApprovedBy` is
  rejected. A `ceo_statement` with no approver is accepted.
- **Invalidate, never overwrite.** After `Supersede(old, next)`: `Get(old)`
  still returns the original statement unchanged, with `Invalidation` set and
  `SupersededBy == next.ID`. `Current` returns `next` and not `old`, and
  `History(next.ID)` returns `[old, next]`. Superseding or invalidating an
  already-invalidated record errors. A simulated failure between the two
  halves of `Supersede` leaves neither half applied.
- **Current / time bounds.** Fixtures before `ValidFrom`, after `ValidUntil`,
  exactly at each boundary, and open-ended each give the expected
  `IsCurrent` result. A record past `ReviewAfter` stays current and appears
  in `DueForReview`.
- **Search / ListByType.** Deterministic order. Type, subject and
  sensitivity filters work. `IncludeHistory` toggles invalidated records.
  `Limit` is honored.
- **Never-store, both directions.** A fixture corpus with at least one
  should-reject case per category (a pasted Gmail body with headers, a
  quoted-reply thread, a two-speaker transcript, a PEM key, a `ya29.` token,
  a JWT, a Luhn-valid card number, an IBAN, an SSN) must be rejected with the
  right `Category`. A should-accept corpus (a statement containing a dollar
  figure, a date, a phone-free short fact, a `vault:` handle, a long but
  non-Luhn invoice number) must be accepted. Each category is also checked in
  `SourceRef` and `Subjects`, not just `Statement`. An `ErrNeverStore`
  message never contains the rejected text.
- **Never-store on read.** A backing file or row edited by hand to contain a
  forbidden value fails to load with `ErrNeverStore`.
- **Twin isolation.** A handle bound to `ceo-demo` cannot see a record
  written through a handle bound to `ceo`, and the bound handle's method set
  has no twin-id parameter. This is asserted with a compile-time interface
  check, as the old `Scoped` design intended.
- **Bounds.** Exceeding `Limits` on live records returns
  `ErrBoundsExceeded`, never silent truncation, and its message does not
  mention the deleted `prune` command.
- **Concurrency.** The existing `TestConcurrentWritersLoseNothing` is ported
  to the new API: parallel writes lose nothing.
- **Still unwired.** A guard test (or `go list -deps` check) asserts that no
  package outside `internal/memory` imports it after F. Wiring is G's job,
  and doing it early would skip G's gate/grant review.

## Dependencies

- **Depends on:** nothing earlier in this redesign. It uses only `audit`
  (for the `Seq` type and meaning), `vault` (for the handle format) and,
  if the owner picks SQLite, `store`'s migration runner. All of these
  already exist.
- **Blocks:** **G** directly (memory access paths need the store) and **Q**
  directly (procedure records). H, I, O and P are blocked transitively
  through G and O.
- **Independent of:** **J** (the durable task record). J can be built before,
  after or alongside F without either touching the other's files, except a
  migration-number choice at commit time if both use SQLite.
- **No new dependency.** Everything here is standard library plus the
  already-approved `modernc.org/sqlite`, and only if the SQLite backend is
  chosen.

## As built (2026-09-25)

These are the details the spec left to the implementation:

- **Files.** `internal/memory/`: `record.go` (the record types,
  `Validate`, `IsCurrent`), `sensitivity.go`, `neverstore.go`,
  `backend.go` (the `Backend`/`Tx` seam), `store.go` (the `Store`
  interface, `Bind`, `Limits` and every rule), `sqlite.go` (the backend),
  and `storetest/` (the contract suite). `internal/store/`:
  `memory_records.go` and `migrations/0014_memory_records.sql`.
- **The Backend seam.** A `Backend` only moves records. Validation, the
  never-store check (on write and on read), bounds, `IsCurrent`, search and
  ordering all live in `store.go`, so a backend never sees an unchecked
  record and a second backend gets every rule for free.
- **`Bind(b Backend, twinID string, l Limits) (Store, error)`.** It takes
  the limits and rejects a malformed twin id.
  `TestStoreMethodSetHasNoTwinParameter` pins `Store`'s method set exactly.
- **`Query.Text`.** This is the search term (whitespace-separated terms,
  all of which must match), and only `Search` uses it. `Current` ignores
  `Text` and `IncludeHistory`.
- **Write refuses** a caller-chosen `ID` (the store assigns one), a set
  `Supersedes` (corrections go through `Supersede`), and a pre-set
  `Invalidation`. A `ceo_statement` must be `WrittenBy: "ceo"`. A twin's
  paraphrase is a proposal and needs an approver.
- **Supersede is transactional.** `Backend.Update` runs inside
  `store.MemoryTx`, a single `BEGIN IMMEDIATE` transaction. Inside it:
  read the old record, check it is live, check bounds, insert the new
  record, then invalidate the old one. Any error rolls back both halves.
  `BEGIN IMMEDIATE` holds the write lock from the start, so a second
  process (the daemon and a CLI) waits on `busy_timeout` rather than
  failing its lock upgrade.
- **Append-only in the database too.** Migration 0014's triggers refuse
  `DELETE`, refuse any update to a content column, and refuse an update to
  an already-invalidated row.
- **Bounds** count records that are not invalidated. A record whose
  `ValidUntil` has passed still counts until it is invalidated, which
  keeps the count independent of the clock. They are checked on write
  only.
- **Never-store details.** Checks run in this order: credential, payment,
  government ID, raw message, length cap, transcript. The first match
  wins. `ErrNeverStore` carries `{Category, Field, Rule}` and never the
  text. Card shapes are a contiguous 13–19 digit run, groups of four, or
  Amex 4-6-5, so dates and phone numbers written with separators don't
  look card-like. Named-speaker transcripts need at least three
  `Name: ...` turns by two or more speakers, one of whom speaks twice, so
  a two-line `Key: value` fact passes. Bracketed `[hh:mm]` timestamps,
  `Speaker N:` and `Assistant:/Human:` markers trip the check sooner.
  Email headers need two distinct header lines, or a single
  `Message-ID`/`Received`-style header.
- **Review hardening (2026-09-25).** The pattern checks scan a normalized
  copy of each field: CR, CRLF and the Unicode line breaks become `\n`,
  Unicode spaces become spaces, dash punctuation becomes `-`, zero-width
  and other format characters are dropped, and full-width and other
  decimal digits become ASCII. The card and SSN checks also treat ASCII
  letters and `_` as separators, so `visa4111...` or `ssn123-45-6789` is
  caught. Hex tokens of 16 or more characters that contain a letter (record
  ids, Gmail ids, commit hashes) are skipped for that, which leaves a
  number glued to an a-f-only prefix (`cc4111...`) undetected. A ref's id
  part refuses Unicode separators and control/format characters, not only
  ASCII whitespace. `approved_by` and `invalidation.by` cannot be `twin`.
  Migration 0014 also refuses `INSERT OR REPLACE` over an existing id,
  which otherwise bypasses the `DELETE` trigger.
- **Council-era files removed.** `memory.go`, `markdown.go`,
  `markdown_test.go`, `lock_unix.go` and `lock_other.go` were deleted with
  `git rm` on the owner's explicit approval (2026-09-25). The build agent's
  own delete was refused by the permission system and correctly left to the
  owner. The temporary `//go:build slicef` tag the new files carried while
  the old ones clashed has been removed, so the default build now compiles
  only the new package, and the stale `prune` error message is gone with
  `memory.go`.
- **The council-era markdown provider.** On the real machine (checked
  2026-09-25), `~/.water/memory/{ceo,coo,cto,design}/session.md` exist, but
  each is byte-identical to the council-era seed file in git history, so
  there is nothing CEO-written to migrate. They were left untouched.

## Owner decisions (decided at Approve, 2026-09-25; provisional where marked)

These were open items until the owner's Approve on 2026-09-25. Each is now
decided. The ones marked **provisional** are set in one place in code,
labelled provisional there, and can change once they are checked against
real records.

1. **Storage backend: SQLite.** This is a new `memory_records` table in the
   existing `water.db`, added by migration `0014_memory_records.sql` (0014
   was still free when F was written). It has the indexes §4 lists:
   `(twin, type)`, `valid_until`, `review_after` and `supersedes`. It adds
   no FTS5 index, because F's search is a deterministic term match. Only
   this backend is built. `docs/CONTEXT.md` principle 6 is amended by a
   dated note, not rewritten. The named-provider registry
   (`Register/Open/Names`) is dropped because there is only one backend.
2. **Sensitivity tiers: `normal | sensitive | restricted`, provisional.**
   The tiers live in `internal/memory/sensitivity.go` and are labelled
   provisional there. They have no behavior; filtering on them is only a
   query parameter.
3. **Government IDs: US SSN only, provisional.** The dashed, spaced and
   bare 9-digit forms are detected, minus SSA's never-issued ranges.
   Passports, driver's licences and non-US national IDs are **deferred**.
4. **Never-store thresholds: provisional, chosen to keep false positives
   low.** They are set in `internal/memory/neverstore.go`:
   - `Statement` (and invalidation `Reason`) is capped at **1000 runes**.
   - An unrecognized-secret token is a run of `[A-Za-z0-9_+=-]` at least
     **40** characters long that mixes upper case, lower case and digits,
     with Shannon entropy of at least **4.3 bits/char**. This check covers
     free-text fields only. Refs get the known-shape checks, because
     opaque provider ids such as Drive's are high-entropy by design.
5. **Retention of invalidated records: keep forever.** There is no
   compaction and no archive. The database enforces it: migration 0014's
   triggers refuse `DELETE`, refuse content updates, and refuse clearing
   an invalidation.
6. **Confidence: stored, no behavior.** Nothing filters or ranks on it.

Source-brief note: §1 of the redesign brief came through the corrupted paste
without any remaining unparseable phrase ("ore distilled facts" was already
reconstructed as "Store distilled facts"). The one real gap is that
"sensitivity" is named as a field with no levels given. That is recorded as
owner decision 2 above (a provisional set), not treated as corruption.
