# Water design kit

The visual identity of Water, taken from "Water: Build, architecture and validation report"
(14 September 2026). Use it for anything visual about Water: pages, posters, slides.

## The line
A council of role-agents on the subscription you already pay for.

## Palette
Colour carries meaning here; it is never decoration.

| Token | Hex | Meaning |
|---|---|---|
| ink | #050505 | the page; near-black, with faint horizontal scanlines as texture |
| cobalt | #0B52A8 | the title block, the CEO, what decides; the only large colour field |
| cream | #ECE8D8 | display type and text on ink |
| signal red | #D81E05 | a thin rule above a title; escalation paths; boundaries |
| terminal green | #25E737 | things that passed: all passing, done, 0 metered |
| graphite | #2A2D33 | the COO node, panels |
| hairline | #7E7E7E | rules, captions, dotted leaders |
| gold | #C9A227 | diagram only: identity files |
| moss | #3E9B4F | diagram only: curated memory |

## Type
- Display: "Helvetica Neue", Helvetica, Arial, sans-serif. Weight 800, tight tracking. WATER set enormous in cream on a cobalt block.
- Labels and data: "SF Mono", Menlo, Monaco, Consolas, monospace. Uppercase, letter-spacing about 0.08em, small.
- Running text, when unavoidable: the display family at regular weight.
- Items are separated by a middle dot: CEO · COO · CTO · DESIGN

## Motifs
- A thin red rule sitting directly on top of a wide cobalt block.
- Monospace key and value lines: `ROLES   CEO · COO · CTO · DESIGN`
- A status panel with a hairline border: uppercase lines ending in DONE, each underlined by a full-width green bar. Dotted leaders run between label and value: `PHASE 1 SKELETON ········ DONE`
- One very large number with a tiny monospace caption beneath it.

## Facts to draw from (use a few, never all)
- Four role-agents. CEO: frame · adjudicate · final. COO: assign · verify · forward. CTO: feasibility · verified fact. Design: user fit · exposure.
- One Go binary. Release v0.1.0-rc.1. Go 1.27. macOS and Linux.
- 16,815 lines of Go · 76 test functions in 23 packages, all passing · 22 skills · 0 metered calls in every real run.
- Runs on the claude or codex subscription CLIs. A metered API is used only by explicit opt-in.
- A brief flows CEO → COO → CTO + Design → COO → CEO. Dissent reaches the CEO word for word.
- Each role has its own soul, memory and skills. No role can read another role's memory.
- Tools are deny-by-default. Every write or command waits for the person at the terminal.

## The seven invariants
1 subscription billing, not metered API · 2 per-role memory isolation · 3 the CEO is the only entry and the only final writer · 4 identity files are fixed anchors · 5 personas hidden from end users · 6 tools deny-by-default and role-scoped · 7 theming never disrupts the chat

## Diagrams
Redraw these as vector shapes in the palette above. Choose at most three.

### A. The permission graph (the strongest single image)
- CEO at top centre: cobalt fill, caption "frame · adjudicate · final".
- COO in the middle: graphite fill, caption "assign · verify · forward".
- CTO bottom left, Design bottom right: outlined, captions "feasibility · verified fact" and "user fit · exposure".
- CEO → COO labelled "direction, decision". COO → CEO labelled "status, forwarded dissent".
- COO and CTO, COO and Design: paired cobalt lines, "assignment" down, "deliverable · status · dissent" up.
- Red dashed curves from CTO and from Design up to the CEO, labelled "escalation only": the safety valves that bypass the COO.
- A dotted hairline between CTO and Design marked "× no specialist-to-specialist edge".

### B. Five kinds of state (isolation)
- Four equal columns: CEO, COO, CTO, DESIGN.
- Each column stacks the same cards: 1 identity files, "fixed · signed · embedded" (gold); 2 curated memory, "bounded · manual writes" (moss); 3 transcripts, "append-only · per role" (cobalt); own inbox (the COO's also has trace:current-run).
- Red dashed walls between the columns, with one red line of text: "No role can read another role's layers 1–3."
- Spanning all columns underneath: 4 run state, the outbox, as a cobalt bar ("typed messages along permitted edges"), and 5 traces as a graphite bar ("every call, message and tool decision; never in a prompt").

### C. One delegated run (a sequence in five steps)
- Lifelines: user, CEO, COO, CTO, Design. Step numbers down the left edge.
- 1: user → CEO, "brief".
- 2: CEO → COO, "direction + brief (verbatim)".
- 3: COO → CTO and COO → Design, "assignment", in parallel. CTO → COO, "deliverable (+ evidence)". Design → COO, "deliverable, dissent". Design to CEO as a red dashed line, "escalation (verbatim, bypasses COO)".
- 4: COO to CEO as a red dashed line, "dissent forwarded verbatim". COO → CEO, "status: VERIFIED / UNCONFIRMED".
- 5: a cobalt pill on the CEO lifeline, "final output". A checkpoint is written after every step.

### D. The harness around the agent node
- Top band, SURFACES: chat TUI · command tree · terminal / JSON · dashboard · diagnose.
- Two panels: ORCHESTRATION (executor, router, state, permission graph) and INTERACTIVE SESSION (slash commands, transcripts, themes, voice).
- Centre, cobalt: AGENT NODE, "the one place a prompt is assembled": own soul + own experience + selected own skills + own frozen memory + only messages addressed to this role + the task.
- Three panels below: ROLE MATERIAL (gold), MEMORY per role (moss), BACKENDS + TOOLS (red).

### E. How experience grows
live exchange → reviewer feedback → one reflection call with a JSON verdict → new, reinforce or none → a support count per lesson → candidates → a reviewed promotion into a skill → re-stamp and journal commit. Nothing in this loop runs while a role is answering.
