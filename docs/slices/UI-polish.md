# Slice: UI polish (restyle to the design reference)

Owner-provided brief (2026-09-28), saved verbatim. **Status: not started.** Per its own first
line, this follows `docs/WORKFLOW.md`: plan first, stop for the owner's approval, then build.
No exploration or planning has happened yet -- this file is the brief only.

---

Follow docs/WORKFLOW.md: plan first, stop for my approval, then build. This is one slice. It
restyles and fixes what exists. It does not add data logic, and it does not include the demo
population.

## What changed since the last prompt

The earlier prompt said "keep the visual style plain, no theming system." **Drop that.** The
result is a correct but generic scaffold. The approved look now exists as real HTML and CSS:

- **`design-reference.html`** (copy it to `docs/design/design-reference.html` and open it in a
  browser). It is the source of truth for tokens (colors, radii, spacing, type) and for every
  component: decision card, both approval cards, the approvals queue, meetings, threads,
  dashboard, project workspace, research pipeline, ideas, and grouped navigation.
- Move its `:root` tokens and component styles into the app's CSS. Reuse the class names where
  you can. Do not invent new colors or spacing. Icons: use Tabler icons, but vendor the SVGs
  locally, since the app must work offline.

## Fixes, from screenshots of the current app

**Decisions**
- Evidence must never show raw email text or email addresses. Show one summarised line per
  evidence item with a small source icon (mail, sheet, Linear). The full source opens under
  "View related data (N)".
- Remove the shouting header "EVIDENCE (INCLUDES EXTERNAL CONTENT, SHOWN AS TEXT)". Mark external
  content with a small icon and a tooltip ("Built from an outside email"). Keep the underlying
  taint field unchanged.
- Do not render the options list on the card (keep it in the data). Add the deadline badge when
  there is a deadline, the action rows with a "Review" button, and the "View related data (N)"
  button.
- Team signal: a small panel to the right of the evidence, hidden if there are no signals.
- When there is no recommendation, show "No recommendation yet: <what is missing>", not
  "None: the evidence does not support one." Keep the validation itself as it is.
- Remove the explanatory paragraph under the page title.

**Approvals**
- Rows per the design: icon, title, one origin line, priority badge, coloured left edge, sorted
  by priority. The detail pane shows the card. The "Select an approval to review it" prompt
  appears in the detail pane only when the list is not empty.
- Empty state: "No approvals waiting. Anything the assistant wants to send or change will
  appear here."
- Person requests get the "Request changes" middle button; agent drafts get "Edit".

**Workspaces**
- Remove internal subtitles like "Template: ideas · Source: research". Use one human line, for
  example "Ideas you haven't turned into work yet."
- Research: three side-by-side columns (Queued / Running / Finished) with cards, as in the
  reference.
- Ideas: a single-row capture bar and a grouped list (Raw / Explored).
- Project workspaces: header card with progress bar and target date, three metric tiles, then
  Needs-you rows. If GitHub isn't connected, show "Connect GitHub" in those tiles, never fake
  numbers.
- Empty states everywhere: one short useful line, not "Nothing here yet."

**Navigation**
- Group as in the reference: Today, Decisions, Approvals, Drafts, Meetings, Threads; then
  Dashboards; then Workspaces split into Projects and Domains. Show counts on Decisions and
  Approvals.

**Cross-cutting**
- No email addresses shown anywhere in the UI. Show a person's name; the address appears only
  in the send read-back.
- Dense rows over big bordered boxes. Cut dead vertical space.

## Design direction (this is what "less basic" means)

The current screens read as generic boxes. The reference fixes that with a few consistent moves.
Apply them everywhere, not only on the screens named above:
- **Stat tiles first.** Every workspace and dashboard opens with 3-4 stat tiles: a label with an
  icon, one large number, and a muted caption. A sparkline only where there is real history.
- **Bento layout.** Mix one wide card and one narrow card per row, 12px gaps. Avoid stacks of
  identical full-width boxes and dead vertical space.
- **One chart per question.** Thin marks, hairline gridlines, one y-axis, no default chart
  legend. Build a small HTML legend that pairs each colour with a line style (solid, dashed,
  dotted), so nothing relies on colour alone.
- **Emphasis over rainbow.** Highlight the one thing that matters in the accent blue and grey
  the rest. Colour carries meaning: status colours are always paired with an icon and text.
- **Dense rows for lists.** Bordered rows, a left priority edge, an icon, a title, one meta line.

## Marketing workspace analytics (build to the "Analytics kit" section of the reference)

Four stat tiles (public rating with sparkline, feedback score, open prospects, cost per lead);
a market-interest line chart with a legend and an "Illustrative" badge; a ranked bar list of
themes people mention; a feedback-mix bar; a content pipeline stepper for the LinkedIn post;
an insight card; prospects and reviews lists with Draft buttons.

Data rules:
- Rating, feedback score, mix, prospects and cost per lead are computed by code from the
  customers sheet (Reviews, Feedback, Accounts) and the finance sheet (brand budget).
- Themes are counted from tagged reviews, tickets and feedback rows. If nothing is tagged yet,
  show the tile as empty, not made-up numbers.
- Market interest stays labelled "Illustrative" until a research job supplies real series; then
  show the job it came from.
- The insight card may only cite numbers the tiles show. Buttons create drafts, never posts.
- Vendor Chart.js locally. The app must work offline.

## Out of scope

Demo seeding, new connectors, new agent logic, the voice/HUD work.

## Acceptance

- Each screen visually matches its section in `design-reference.html` at the same width.
- Screenshot every screen (Today, Decisions, Approvals, Drafts, Meetings, Threads, a dashboard,
  a project workspace, Research, Ideas) before and after, and show me the pairs.
- No raw email addresses or internal labels appear on any screen. Existing tests pass.

---

## Findings (Explore phase, 2026-09-28)

The current web UI (`internal/webui/static/`) is a working, plain-CSS scaffold — not a blank
slate. Its class names (`.card`, `.row`, `.badge`, `.avatar`, `.trail`, `:root` tokens) already
loosely parallel the design reference's vocabulary, so this is a restyle/rename/extend pass, not
a rewrite. Several of the brief's requested fixes are **already done**; others are exact,
small, located bugs. Per screen, with file:line:

- **Decisions** (`view_decisions.js`):
  - The "shouting" header is real: `view_decisions.js:105` renders literally `'Evidence
    (includes external content, shown as text)'` when `isUntrusted`. Fix: replace with plain
    `'Evidence'` plus a small `ext-glyph`-style icon+tooltip (the glyph mechanism already exists
    at `app.js:167`, `'Built from an outside email'` — reuse it, don't invent a second one).
  - The Options list **is** rendered on the card today: `view_decisions.js:124-132`. Remove the
    `el.appendChild(...)` call (keep `options` in the data/response, only stop rendering it).
  - "No recommendation yet: ..." is a one-line change at `view_decisions.js:137-140`: replace
    `rec || 'None: the evidence does not support one.'` with a template using the card's own
    already-computed `gaps` array (`view_decisions.js:141`) for the "what is missing" part —
    no new data needed, `gaps` is already fetched and rendered separately today.
  - Evidence lines (`view_decisions.js:97-103`) **already** show one summarised line + a kind
    icon, no raw source inline, no email addresses — this part of the brief is already met.
  - The explanatory paragraph under the title is `S.header('Decisions', 'Approving a card is
    two steps...')` at `view_decisions.js:60` — drop the second argument.

- **Approvals** (`view_approvals.js`, `app.js`):
  - "Select an approval to review it" **always** shows regardless of list emptiness —
    `view_approvals.js:45` sets it unconditionally in the same `Promise.all` that fetches the
    list, before `items.length` (computed at line 48) is known. Real (small) code restructure
    needed: defer the placeholder until after the count is known, not just a CSS/text change.
  - Empty-pending text is a one-line string change: `view_approvals.js:54`.
  - "Edit" vs "Request changes" **already** exist and are already correctly split by
    envelope type (`app.js:605-616`) — already met.
  - Dense rows with an icon + colored left edge: `approvalRow` (`app.js:467-491`) has no icon
    and no priority-colored edge today — `.row.p-urgent`/`.p-high` (existing CSS,
    `app.css:300-302`) is the same concept the reference's `.row.edge-danger`/`.edge-warn` wants;
    likely a rename/extend rather than a new mechanism. Needs a function-name -> icon map (the
    same `KIND_ICONS` idea `view_decisions.js:25` already uses for evidence, extended to
    approval actions).

- **Navigation** (`index.html:40-43`): today is one flat collapsible "Workspaces" disclosure,
  not split into Projects/Domains. This **is** achievable as a pure frontend grouping change,
  no new server data: `twins/ceo/workspaces/*.yaml` already has a `template:` field
  (`project` for the six project workspaces; the six domain workspaces — clients, finance,
  ideas, marketing, people, research — use their own domain-specific template name), which
  `GET /v1/workspaces` already returns. Group client-side on `template === 'project'` vs not.

- **CSP blocks the reference's CDN approach entirely, confirming the brief's own instruction.**
  `index.html:5`'s CSP is `script-src 'self'; style-src 'self'; img-src 'self'`, no exceptions —
  the reference's `cdn.jsdelivr.net` Tabler-icons and Chart.js `<script src>` tags will not
  load at all under this policy. Icons and Chart.js **must** be vendored as local files under
  `internal/webui/static/`, exactly as the brief says, not optionally.

- **The Marketing analytics kit needs real new server-side computation, not just restyling —
  the single biggest finding.** Read `internal/gateway/workspace_detail.go`'s full Marketing
  section (`marketingTrendTiles`, `marketingProspectsTile`, `marketingPublicReviewsTile`,
  `marketingCapacityNote`, lines ~1781-2020, all from Phase 5d). None of the four new stat
  tiles' data exists today: `marketingPublicReviewsTile()` (line 1929) is a bare, ctx-less
  function that **always** returns `not_connected` with zero items — Phase 5d's own doc comment
  says no public-reviews data source existed at all at the time. There is no public-rating, no
  feedback-score, no cost-per-lead, no "themes people mention" computation anywhere in this
  codebase. The brief's own data rules point at exactly the fix: the owner's real
  `Renaissance_Customers.xlsx` (connected 2026-09-27/28, this session) has `Reviews`, `Feedback`
  and `Accounts` tabs the `company_customers` connector (`internal/connectors/google/gsheets/
  customers.go`) does not read yet — only the `Accounts` tab, one function (`accounts`). Building
  the analytics kit's tiles for real means: (a) extending `company_customers` with new read
  functions for `Reviews`/`Feedback` (a read-extension to an existing connector, not a new one,
  per this repo's own established precedent — Linear's read-extensions were treated the same
  way in Phase 4), and (b) new `internal/dashboards`-style compute functions. That is materially
  more than a CSS pass and does not belong in a slice the brief itself scopes as "restyles and
  fixes what exists... does not add data logic."

## Scope and non-scope

**In scope:** token/component CSS migration from `docs/design/design-reference.html` into
`app.css`; per-screen markup fixes listed above and in the brief, each traced to its own
file:line; local vendoring of the Tabler icon subset actually used and of Chart.js; the
Projects/Domains nav split (pure grouping, no new data).

**Out of scope, confirmed:** demo seeding, new connectors beyond read-extensions the analytics
kit strictly needs, new agent logic, the voice/HUD work (per the brief's own words) — **and**,
newly flagged here: the Marketing analytics kit's four new data tiles, because computing them
for real requires the customers-connector extension and new compute functions described above,
which is data logic the brief explicitly excludes. Recommended default (open question below):
split the analytics kit into its own follow-on slice fed by real data, and ship the *layout only*
here with every tile in its already-established, honest `illustrative`/`not_connected` state
(the exact convention `marketingTrendTiles` already uses today) until that data work lands.

## Design

- Merge the reference's `:root` tokens into `app.css`'s existing `:root` block, keeping the
  existing dark-mode `@media (prefers-color-scheme: dark)` override structure (already present,
  `app.css:27-48`) rather than introducing a second theming mechanism -- CLAUDE.md-adjacent
  principle here is "don't invent a second system for what one already does."
  Reference tokens have no direct existing equivalent for a couple of names (`--ok`, `--danger`
  used as icon/text colors vs. today's `--ok-border`/`--danger` used as borders) -- reconcile
  by adding the missing few rather than renaming the many call sites that already use the
  existing names correctly.
- Icons: audit exactly which Tabler glyphs the reference actually uses (`ti-report-money`,
  `ti-mood-sad`, `ti-clock`, `ti-mail`, `ti-flag`, `ti-paperclip`, `ti-shield-half`,
  `ti-microphone`, `ti-message-circle`, `ti-scale`, `ti-bug`, `ti-git-pull-request`,
  `ti-git-merge`, `ti-lock`, `ti-bulb`, `ti-star`, `ti-mood-smile`, `ti-target-arrow`,
  `ti-coin`, `ti-alert-triangle` -- roughly 20, not the whole Tabler set), download only those
  SVGs to `internal/webui/static/icons/`, and add a tiny inline-SVG helper in `dom.js` rather
  than an icon font (an icon font is one CSS `@font-face` file for ~1600 glyphs when ~20 are
  used; inline SVG is smaller, and this app's existing glyph approach, `KIND_ICONS` in
  `view_decisions.js`, is already plain-Unicode-character-as-icon, so inline SVG is the more
  consistent upgrade path, not a bigger dependency).

## Task list (small, dependency-ordered, each its own commit + screenshots)

1. Token/base CSS merge (`app.css` only); confirm no visual regression on every existing screen
   before any component changes (baseline screenshots first).
2. Icon vendoring mechanism (SVG files + a `dom.js` helper), proven on one screen (Approvals'
   row icons) before reuse everywhere.
3. Decisions: remove the shouting header, remove the options-list render, fix the
   no-recommendation text, drop the explanatory paragraph.
4. Approvals: fix the select-prompt/empty-state ordering bug, add row icons + priority edge.
5. Meetings/Threads/Drafts: dense-row pass, empty-state text pass (smallest, most mechanical).
6. Workspaces: Projects/Domains nav split; project-workspace header card + stat tiles
   (`internal/webui/static/view_workspaces.js` -- not yet read in this pass, needs its own
   file:line audit before this task starts); Research's three-column layout; Ideas' capture bar
   + grouped list.
7. Dashboards: bento layout, one-chart-per-question pass (Finance/Delivery/Clients -- existing
   data, no new compute).
8. Marketing analytics kit: **layout only**, per the scope note above, pending the owner's
   answer to the open question -- every tile in its honest degraded state, no new connector
   reads in this task.

## Acceptance criteria

- Per `docs/WORKFLOW.md`: not "works." For each of the 10 named screens, a before/after
  screenshot pair at the same viewport width, visually matching the corresponding
  `design-reference.html` section.
- `grep -rn "@"` (a bare email-shaped string) across rendered DOM output/fixtures for Decisions,
  Approvals, and Workspaces finds none outside the existing `.readback` element (the one
  explicit, brief-approved exception).
- `go test -count=1 ./...`, `go vet ./...`, `CGO_ENABLED=0 go build ./cmd/water`, `gofmt -l`
  all green (no Go changes expected, but the embed and any Go-side fixture tests must still
  pass); no JS test suite exists today, so front-end verification is the screenshot pairs plus
  a manual click-through of every fixed interaction (options no longer shown, no-recommendation
  text, approvals empty-state ordering).

## Invariant checks

- The external-content marker changes presentation only -- `isUntrusted`/the taint field
  itself, and every place that reads it server-side, is untouched; only the DOM it produces
  changes (icon+tooltip instead of a header string).
- No fabricated numbers: every Marketing analytics tile (whichever ship in task 8) must use
  this codebase's existing `not_connected`/`illustrative`/empty-state convention
  (`dashboards.TileState`) exactly as `marketingTrendTiles` already does -- never a static
  placeholder number rendered as if real.
- CSP (`index.html:5`) stays `'self'`-only for script/style/img -- both new dependencies
  (icons, Chart.js) must be vendored as local files under `internal/webui/static/`, never
  referenced by URL, so no CSP relaxation is needed.

## Risks and open questions

1. **The Marketing analytics kit's data gap (above).** Proposed default: ship layout/empty-state
   only in this slice; the real `Reviews`/`Feedback` reads become their own small follow-on
   slice (a `company_customers` read-extension, matching this repo's own "a read-extension to
   an existing connector is not a new connector" precedent from Phase 4/U4). **Needs the
   owner's explicit choice**, since the brief's own data rules assume the data already exists.
2. **Chart.js as a new vendored frontend file.** CLAUDE.md's "ask before adding anything" reads
   as being about Go module dependencies (the binary's own build), but Chart.js would be new
   third-party code shipped and embedded in the same binary via go:embed, executed in the
   webview. Flagging this explicitly for the owner to confirm rather than assuming it's covered
   by the existing Go-dependency approval, since it's a different kind of dependency (JS,
   client-executed, not vetted the way `modernc.org/sqlite`/the MCP SDK were).
3. **`view_workspaces.js` (902 lines) was not read in this pass** -- it's the largest view file
   and covers Project/Finance/Clients/People/Ideas/Research/Marketing workspace rendering, i.e.
   most of the brief's "Workspaces" fixes. Task 6 above needs its own Explore sub-pass before
   work starts; treating it as "more of the same pattern" without reading it first would be a
   guess, not a plan.
4. **Approvals' select-prompt fix is a small code restructure, not pure CSS** (finding above) --
   flagged so it isn't treated as a copy-paste text change during implementation.

**Status: plan complete, waiting for the owner's approval. No code written, no CSS/HTML/Swift
changed, nothing committed.**
