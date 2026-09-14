---
schema: 1
role: fourth
kind: memory
---
<!--
Bounded session memory (Hermes pattern). Managed ONLY via `water memory fourth add|remove|prune`.
Loaded once per run as a frozen snapshot. No automatic growth, decay, or scoring.
Entry format:
## [<id>] <RFC3339 timestamp> #tag1 #tag2
<free text, one or more lines>
-->
