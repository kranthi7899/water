# Water document design system

Everything Water produces that a person reads (emails, reports, memos, policies, meeting recaps,
briefs) shares one identity. The email is the one-screen version. Documents are the scrolling
version, so they get layers: a cover, section openers, tiles, callouts, figures, a consistent footer.

## 1. Identity elements

| Element | Use |
|---|---|
| Wordmark `water-header.svg` | Top-left of every cover and email header. Never recoloured. |
| Koi mark `water-koi.svg` | Once per document, on the cover (bottom right, over the glass band) and once per email (footer). Never in running headers. |
| Glass band `glass-band.png` | Cover base and email footer only. Always darkened so text over it stays readable. |
| Tagline | "Trust the flow." Nothing else. |
| Disclosure | "Prepared by Water, an AI assistant, for <name>." (documents) · "Sent by Water, an AI assistant, on behalf of <name>." (email). Once, in the footer. |

## 2. Palette

Blue is the identity; the companions were chosen to sit well next to it and to carry meaning in
diagrams, not to compete.

| Token | Hex | Role |
|---|---|---|
| ink | `#0B0B14` | Cover, dark callouts, audit/trace in diagrams |
| paper | `#F6F7FA` | Page tint for tiles and light callouts |
| white | `#FFFFFF` | Body pages |
| blue-sky | `#6CCBFF` | Section numbers, top band of the wordmark |
| blue | `#3B94F2` | Primary accent: rules, links, tile edges, primary nodes |
| blue-deep | `#2D5FB8` | Kickers, secondary emphasis |
| navy | `#173561` | Table heads, sub-headings, shared/system nodes |
| teal | `#1BA79A` | Data and memory; success states |
| violet | `#6D5BD0` | Agents, roles, identity |
| amber | `#E0A13A` | Attention, warnings, "watch" callouts |
| coral | `#E2634E` | Errors, failures, blocked |
| slate | `#6B7280` | Structure, neutral nodes, captions |

Diagram rule: at most three companion colours per figure plus blue and slate. Colour encodes a
category (all agents violet, all memory teal), never sequence. Every coloured diagram carries a
one-line legend. Text on a coloured fill is white or the ink; never grey.

## 3. Typography

| Use | Face | Size |
|---|---|---|
| Titles, section names, tile numbers, kickers | Archivo Expanded ExtraBold (800) | Title 30pt · section 18pt · tile 20pt |
| Sub-headings, table heads, small labels | Archivo Expanded SemiBold (600) | 10.5pt / 8pt with 0.08–0.2em tracking |
| Body, captions, tables | System sans (Helvetica Neue, Arial) | Body 10.5pt/1.55 · table 9.2pt · caption 8.5pt |
| Code, ids, commands | System mono | 9pt on a pale blue chip |

Archivo Expanded is SIL Open Font License; embed the two static instances with the renderer.
Kickers are uppercase with wide tracking; nothing else is uppercase.

## 4. Page anatomy (A4, margins 22/18/20/18 mm)

**Cover** (ink background): wordmark top-left · blue gradient rule · title and tagline · fact list
with dotted leaders (roles, release, date, status, whatever the document type needs) · status lines
in teal for DONE · glass band across the bottom third with the koi over it, bottom right.

**Contents**: kicker "Contents", dotted-leader list with page numbers.

**Section opener**: oversized two-digit number in blue-sky beside the section name in Archivo, one
grey line of description, a 3px blue rule. Sections start on a new page in reports; in memos they
run on.

**Body components**
- Stat tiles: paper background, 3px blue left edge, big Archivo number, grey caption. Rows of 3–4.
- Callout (dark): ink background, blue left rule, kicker in blue-sky. For the finding that matters.
- Callout (light): paper background, kicker in blue-deep. For "in practice" notes.
- Watch callout: dark with an amber kicker. For risks and limits.
- Tables: navy head row in Archivo SemiBold, hairline row rules, alternating pale rows. No vertical
  rules.
- Figures: SVG, palette above, legend below, caption "Figure N." in grey.
- Code chips for commands, ids and file names.

**Footer** (every page except the cover): left "Water · <document title> · <version>", right page
number, 9px grey. The disclosure line goes on the last page above the footer.

## 5. Document types

All types share the identity, palette, type and footer. They differ in how much of the anatomy
they use.

| Type | Cover | Contents | Section openers | Typical components | Length |
|---|---|---|---|---|---|
| Report (architecture, validation, research) | Full | Yes | New page each | Tiles, dark callouts, tables, figures, appendix | 10–40 pages |
| Brief / memo | Compact: wordmark, title, one-line purpose, date; no glass, no koi | No | Run-on | One tile row, one callout, a table | 1–3 pages |
| Policy | Compact cover with version and effective date | Yes if > 4 pages | Run-on | Numbered rules as a table, implementation map figure, owner list | 2–6 pages |
| Meeting recap | No cover; header block with meeting name, date, attendees | No | Run-on | Four fixed sections: Decisions made, Action items (owner, date), Open questions, FYI | 1–2 pages |
| Decision record | Compact cover | No | Run-on | Evidence table with sources, options, recommendation callout, actions | 1–3 pages |
| Investor update | Full cover, koi | No | Run-on | Tiles (cash, burn, runway), one chart, lowlights as a watch callout | 2–4 pages |

The koi appears on full covers only. Compact covers use the wordmark alone, so short documents stay
light.

## 6. Email (for reference)

Header: wordmark image on ink. Body: white panel, system sans. Footer: signature block, then the
glass band with the koi and the disclosure. Defined in `email-template.html`.

## 7. Accessibility and print

Body contrast at least 7:1 on white; white on navy/ink for heads. Figures never rely on colour
alone (labels in every node). Print on white paper: cover stays dark by design; a `--no-cover-ink`
option renders the cover on white with the koi and wordmark for cheap printing.
