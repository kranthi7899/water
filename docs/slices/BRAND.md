# Slice: Water brand system (email and documents)

Owner-provided brief (2026-09-28), saved verbatim below. **Status: plan only, waiting for
approval.** Per the brief's own process: this file is step 1 (read + plan). No code has been
written. Phase A builds only after this plan is approved; Phase B builds only after Phase A is
separately built, reviewed and approved.

---

[The owner's brief — "Water brand system: email and documents", starting "Paste into Claude Code
in the Water repo. This supersedes brand-email-prompt.md and document-system-prompt.md..." — is
reproduced in full in the conversation this plan was written from. It is not re-pasted here to
keep this file to a reasonable size; the task list and acceptance criteria below are copied from
it verbatim where it matters.]

---

## Findings

**Assets actually provided vs. the brief's own list.** The brief's "Files I'm giving you" section
names a `brand/` folder with 20 files. What actually landed in `docs/design/brand/` is 8:
`water-report-sample.pdf`, `pages-preview.png`, `documents.md`, `koi-on-ink.png`,
`water-header.png`, `footer-preview.png`, `email-preview.html`, `water-koi.svg`. **Missing, and
needed before Phase A can actually be built:**
- `water-koi-288.png` — the brief's own spec for the email footer inline image (`Content-ID:
  <water-koi>`). `water-koi.svg` exists but the brief's A1 explicitly calls for the PNG.
- `glass-band.png` / `glass-band-600.png` — the email footer background (`Content-ID:
  <water-glass>`, A1) and the document cover base (documents.md §4). Only a composited preview
  (`footer-preview.png`, koi already laid over the band) exists, not the band itself as a
  separate asset.
- `email-template.html` / `email-template.txt` — the brief's own Go-template source files.
  `email-preview.html` (sample data baked in) exists and was rendered/read for this plan, so the
  visual target and rough structure are known, but the actual template source with `{{}}`
  placeholders does not exist yet and needs to be authored fresh in Phase A, not extracted.
- `gen_mark3.py`, `cover-preview.png` — not needed for Phase A; `water-header.svg` (only the PNG
  was given) would matter if Phase A needs to regenerate the wordmark, which it doesn't (it only
  *uses* the given PNG).
- The `documents/` folder (fonts, HTML source beyond the one sample PDF) — irrelevant to Phase A,
  needed before Phase B can build.

None of this blocks writing this plan (the brief's request), but Phase A's task A1 (inline images)
cannot be finished without `water-koi-288.png` and `glass-band-600.png` specifically — flagged as
an open question below, default: ask the owner for them before starting A1's build task.

**gmail.go's actual MIME building (`internal/connectors/google/gmail/gmail.go`).**
- `buildRawMessage` (gmail.go:703-748) is the one function that builds the raw RFC 2822 message
  for `draft_message`/`send_message`/`draft_for_review`. Today it produces either a single
  `text/plain` part, or (when an HTML alternative is given) `multipart/alternative` with a plain
  and an html part — never `multipart/related`, never any inline image, never a `Content-ID`
  anywhere in the codebase. A1's work is real, new structure here: wrap today's
  `multipart/alternative` output inside an outer `multipart/related`, then add three more parts
  (the header, koi and glass-band images) each with a `Content-Disposition: inline` and
  `Content-ID: <name>` header the HTML template references as `cid:name`.
- **A disclosure line already exists and already runs through this exact path** —
  `appendSignature`/`plainSignature`/`htmlSignature` (gmail.go:639-671), called from both
  `draftMessage` and `sendMessage` (never `draftForReview`, by design — see its own comment at
  gmail.go:538-548). Today it appends a plain `"Sent by Water, an AI assistant, on behalf of
  <name> — approved before sending."` line after a `<hr>`, where `<name>` is
  `g.signatureName` = config's `agent.signature_name` (`internal/config/config.go:255-262`, a
  single string field, no title/company/links/CTA). **This is the single source of the disclosure
  today (A3 already holds structurally: there is exactly one call site)** — the brief's new
  signature block (name/title/company/signoff/links/cta) and the template's own footer disclosure
  supersede this whole mechanism, not add a second one alongside it. Open question below: what
  happens to `agent.signature_name`/`agent.mail_address` once `twins/ceo/brand/signature.yaml`
  exists — recommend retiring `agent.signature_name` in favor of `signature.yaml`'s `name` field
  (one source of truth for "the CEO's name"), keeping `agent.mail_address` (a different concern:
  which mailbox sends, not who's in the signature).
- `readWriteArgs` (gmail.go:624-637) already runs the recipient/spokenemail checks before
  anything is built — A1-A5 don't touch this, it stays exactly as-is.

**No PDF generation exists anywhere in this codebase**, confirmed by `find internal/reports` (only
`reports.go`/`reports_test.go`) and reading `reports.go` in full. `internal/reports.Render`
produces a single self-contained **HTML** string (inline `<style>` only, `html/template` so
untrusted content is escaped) from a `Report{Title, Sections[]}` tree where each `Section` is a
heading plus paragraphs or one table — no PDF output, no stat tiles, no callouts, no figures, no
cover, no footer/page-numbers, no font embedding. `Render`'s own doc comment states a "pure
renderer" contract (caller sources every value, never fetches or invents) that matches the
brief's B3 exactly, so this package is a real, reusable foundation for Phase B, but it needs
either a substantial extension or a sibling package (`internal/docs`, per the brief's own B1
wording) for everything past plain paragraphs/tables, plus an actual PDF backend, which doesn't
exist in any form today.

**A conflicting, already-obsolete design system exists and must be explicitly retired.**
`reports.go`'s own doc comment cites `docs/design-kit/water.md` ("the Water design kit") as its
visual reference — cream page, cobalt title blocks, signal-red rules, terminal-green pass/fail,
Helvetica/SF Mono. Reading that file confirms it predates the CEO-twin rebuild: its own tagline is
literally "A council of role-agents on the subscription you already pay for," which is factually
wrong today (the four-role council was deleted months ago per `docs/CONTEXT.md`). **The new brand
system in this brief supersedes `docs/design-kit/water.md` outright** — not an open question, but
worth saying explicitly in Phase B's scope so nobody tries to reconcile the two palettes.

**The approval payload hash is already fully generic — no core change needed.**
`approvals.PayloadHash(payload map[string]any) (string, error)` (`internal/approvals/queue.go:
235-241`) is `canon.Hash` (sha256 of the canonicalized, sorted-key JSON) over *whatever map it's
given*. The shared foundation's item 3 ("the approved payload hash covers the body, the template
version, the signature config and the asset hashes") requires **zero changes to `PayloadHash` or
`Queue` itself** — it only requires that whichever code proposes the email-send envelope
(currently: wherever `gmail.send_message` gets staged, e.g. `internal/gateway/workspace_decisions.go`'s
`Propose` call or the equivalent path for a plain approval) includes `template_version`,
`signature_hash` and `asset_hashes` as additional keys in the `Envelope.Payload` map alongside the
existing `to`/`subject`/`body`. Any edit to any of those keys already voids the approval by
construction, exactly as the brief asks. Phase B's document-tree hash works the same way.

**go:embed precedent.** Three packages already use it: `internal/webui/webui.go` (`//go:embed
static`, a whole directory — the closest precedent for `internal/brand`'s image/template assets),
`internal/store/store.go` (migrations), `internal/nervous/eval/eval.go`. None embed a font file
today, but the mechanism (embed a directory, serve via `embed.FS`) is identical to what
`internal/brand` needs for the two Archivo Expanded static instances plus the SVG/PNG assets.

## Scope and non-scope

**In scope (both phases, per the brief):** `internal/brand` (design tokens, embedded assets,
signature config loading, asset hashes, template version); `twins/ceo/brand/signature.yaml`;
extending `approvals.Envelope.Payload` (not `PayloadHash` itself) to cover template
version/signature/assets; Phase A's email MIME/template/sanitizing/approval-card work; Phase B's
document renderer, once separately approved.

**Out of scope, per the brief's own list:** LinkedIn posts, Linear comments, text messages (no
header surface); restyling the Water app (that's the separate, already-shipped UI-polish slice);
slide decks, DOCX output; new connectors; changing who Water may email; changing what the model
is allowed to say.

**Also out of scope for this plan specifically:** anything Phase B until Phase A ships and the
owner explicitly says go, per the brief's own 3-step process.

## Design

**`internal/brand`** (new package): embeds `docs/design/brand/`'s final asset set via `go:embed`
(mirroring `internal/webui`'s pattern), exposes:
- A single Go struct/table of the palette + type scale from `documents.md` §2-3 (the twelve hex
  tokens, the Archivo/system-sans sizes), so both the email template and — later — the document
  renderer generate CSS from one source, never a second copy of the hex values.
- `Signature` loaded from `twins/ceo/brand/signature.yaml` (fields exactly as the brief's example:
  `Name, Title, Company, Signoff, Links []{Label,URL}, CTA *{Label,URL}` — CTA a pointer so it's
  cleanly absent when unset, not a zero-value struct that renders an empty button).
- `AssetHash(name string) string` and a `TemplateVersion` constant, both for the payload-hash
  extension above.

**`twins/ceo/brand/signature.yaml`**: exactly the schema in the brief, seeded with `example.com`
placeholder values (never real ones, matching every other seed file in this repo).

**Approval payload extension**: wherever `gmail.send_message`'s envelope gets proposed, add
`template_version`, `signature_hash` (hash of the loaded signature config) and `asset_hashes` (a
map or joined hash of the header/koi/glass-band assets actually used) to the payload map before
`Queue.Propose`. No change to `approvals.PayloadHash` or the `Envelope` struct's stored fields —
this is purely what the caller puts in `Payload`.

## Task list — Phase A only (small, one commit each)

1. **`internal/brand` skeleton**: the design-token table, `go:embed` wiring, `Signature`
   loader + its YAML schema, `AssetHash`/`TemplateVersion`. Tests: token table round-trips to CSS
   custom properties; a malformed `signature.yaml` fails loudly at load, not silently; the CTA
   pointer is nil when the key is absent from YAML.
2. **`twins/ceo/brand/signature.yaml`** seeded with `example.com` placeholders, loaded by task 1's
   loader; a test that the real (non-demo) twin's manifest loads it without error.
3. **Email HTML/text templates**: author `email-template.html`/`.txt` fresh (the brief's own files
   don't exist yet — build from `email-preview.html`'s already-seen structure plus
   `documents.md` §6), referencing `cid:water-header`/`cid:water-koi`/`cid:water-glass` and the
   signature block fields. **Blocked on the owner supplying `water-koi-288.png` and
   `glass-band-600.png`** (see Findings) — this task cannot finish without them.
4. **`buildRawMessage` → `multipart/related`**: extend gmail.go:703-748 to wrap the existing
   `multipart/alternative` plain/html pair inside `multipart/related`, adding the three inline
   image parts with `Content-ID`/`Content-Disposition: inline`. Tests: the golden-file MIME output
   parses with `mime/multipart` and has the expected part count/structure; images have
   `width`/`height`; header alt text is exactly "Water. Trust the flow."; footer koi alt is
   "Water"; the glass background has an ink `bgcolor` fallback attribute.
5. **Retire the old disclosure mechanism, wire the new one**: replace
   `appendSignature`/`plainSignature`/`htmlSignature` (gmail.go:639-671) with the brand template's
   own signature block + footer disclosure, sourced from `internal/brand.Signature`, appearing
   exactly once (A3). Decide and implement the `agent.signature_name` question (recommended
   default: retire it, `signature.yaml`'s `Name` is the one source).
6. **Closer-stripper guard (A2)**: strip a trailing "Thanks,"/"Thank you,"/"Best,"/"Regards,"
   (etc.) plus a following name line from model-drafted bodies, so the template's own signoff
   can never double up. Tests: exactly the cases the brief names, including the two-line
   `"Thank you,\nKranthi"` form.
7. **Body sanitizing (A5)**: allowlist `p,br,a,strong,em,ul,ol,li`; links restricted to
   `http`/`https`/`mailto`; no `img`/`style`/`script`. Plain-text part derived from the same
   sanitized body. Tests: `<script>`, `<img>`, `javascript:` links, and inline `style` attributes
   all come out clean.
8. **Approval card (A4)**: the body as today, a "Water header and signature will be added" note,
   a sandboxed-iframe (no scripts, no remote images) "Preview full email" control rendering the
   final HTML. Spoken read-back stays body-summary-only, unchanged.
9. **Payload-hash extension**: add `template_version`/`signature_hash`/`asset_hashes` to the
   envelope payload at the propose call site (task/finding above). Test: editing the signature
   config, template version, or an asset changes the resulting `PayloadHash`.

## Phase B sketch (not started; full task breakdown deferred to its own planning pass after A ships)

Per the brief's B1-B8: a renderer (extend `internal/reports` or a new `internal/docs`, decision
deferred — see the open dependency question below), Go templates ported from
`documents/report-template-sample.html` (not yet supplied), the typed `DocType`/`Section`/block
tree, code-built SVG figures from the token palette, the cover/footer/disclosure rules, rewiring
the five named producers (demo policy doc, meeting recaps, decision records, research reports,
investor update) onto the one renderer and deleting their per-producer styling, and the same
payload-hash-covers-the-document-tree extension as Phase A's email.

## Acceptance criteria

**Phase A** (copied from the brief verbatim):
- Send a real test email to the owner's own address. Screenshot it in Gmail (web) and Apple Mail,
  light and dark mode. Note anything that looks off.
- With images blocked, the header shows its alt text on the ink band and the footer is a plain ink
  band with the disclosure still readable.
- Existing tests pass. Report what changed in the send path.

**Phase B** (copied from the brief verbatim, for whenever it starts):
- Regenerate the build/architecture report through the new renderer and show it next to
  `water-report-sample.pdf` — same structure, all real content.
- Render one each of policy, recap, decision and investor update from real seed data and
  screenshot their first pages.
- Report which producers were rewired and what per-producer styling was deleted.

## Risks and open questions

1. **Missing assets block Phase A task 3 specifically** (`water-koi-288.png`,
   `glass-band-600.png`). Default: ask the owner for them before starting that task; everything
   else in Phase A (tasks 1-2, 4-9) can proceed without them using placeholder/existing assets
   where a real file is only needed for the final template wiring.
2. **Phase B's PDF-rendering engine is a new dependency, not pre-approved.** The brief's B1 offers
   "headless Chromium if already in the toolchain, otherwise a Go library that supports `@page`
   margins and running footers" — headless Chromium is NOT in this toolchain today (nothing in
   `go.mod` or the repo references it), and no Go PDF library is currently a dependency either.
   CLAUDE.md's dependency rule ("the approved new ones are `modernc.org/sqlite` and
   `github.com/modelcontextprotocol/go-sdk`. Ask before adding anything else") is written for Go
   module dependencies, and either choice here is exactly that — this needs the owner's explicit,
   separate go-ahead before Phase B's task list is finalized, not a default silently picked here.
   No recommendation is made in this plan; it's flagged for the owner to decide once Phase B
   planning actually starts.
3. **`agent.signature_name`/`agent.mail_address` overlap with `signature.yaml`.** Recommended
   default: retire `agent.signature_name` (config-level) in favor of `signature.yaml`'s `Name`
   field (twin-level, richer); keep `agent.mail_address` unchanged (a different concern — which
   mailbox sends). Flagged since it's a small backward-compatibility decision, not obviously
   forced either way.
4. **`docs/design-kit/water.md` is superseded outright**, not reconciled — confirmed obsolete
   (pre-dates the CEO-twin rebuild, cites the deleted four-role council). No action needed in
   Phase A; Phase B's task list should explicitly retire or clearly mark this file rather than
   leave two conflicting "the Water design kit" documents in the repo.
5. **`email-template.html`/`.txt` must be authored fresh**, not extracted from a supplied file —
   the brief's own file list implies they'd be handed over; they weren't. The visual target
   (`email-preview.html`, already read and rendered) and `documents.md` §6 are enough to build
   from, but this is real authoring work, not a copy.

---

Waiting for the owner's approval. No code written. Phase B additionally waits until Phase A is
built, reviewed, and separately approved — per the brief's own 3-step process.
