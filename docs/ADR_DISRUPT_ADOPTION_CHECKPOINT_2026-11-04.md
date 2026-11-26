# Checkpoint: DISRUPT ideas mapped to existing project roadmaps
Date: 2026-11-04
State: PLANNING_CHECKPOINT; no feature implementation, tests, release, deployment or routing mutation.
Observed MCPshka API: 0.7.1.
Project: /root/repos/mcpshka
ROADMAP SHA256 after edits: d3e346c4f4e6615dda7ef369ea25fb4c73507068cf77ba8a4a0466087733e41a

## User decisions
- Do NOT build AI NPC, Game Companion, AI Garage or generative quests for offline Doomchik, quakaII or NFSU2. Preserve offline original game delivery.
- Put easy-to-understand natural-language help and optional voice into DIAG and Lane. The request can ask for an explanation or for a typed, previewable, permissioned action; no shell execution from untrusted input.
- MCPshka is a shared optional executor/knowledge/agent layer, not a mandatory dependency of Lane, DIAG or offline games.

## DISRUPT source -> primary owner -> existing phases
- Memento -> MCPshka P10/P12: provenance-linked project memory and handoff packets; DIAG P36/P46: incident memory, corrections.
- Multitool -> MCPshka P26: agent teams / review and acceptance; DIAG P63/P65 and Lane UX1/UX2: natural language to typed intent.
- AI Concierge -> MCPshka P18/P29: conditional/scheduled jobs with durable receipts and brief event summaries; Lane UX2: plan/apply via laned, not MCPshka shell.
- Тайп / GigaAM -> DIAG P64 and Lane UX3: opt-in local voice frontends; MCPshka P25 only transport/integration, no duplicated speech engine.
- Zoobr -> DIAG P52/P63 and Lane UX1: comprehensible factual explanations of observed state.
- Radar -> Lane UX-R: pinned upstream candidates; MCPshka P24: cross-project research routing; DIAG P61: source/licence validation.
- Короче -> concise summaries over evidence in MCPshka P18, DIAG P63 and Lane UX1; no separate news app.
- Narra, Гочи and Game Companion -> explicitly rejected for games; no implementation tasks or game release dependencies.
- Réka, Айва, ЗИП -> no current engineering scope.

## Checkpoint / resume
- MCPshka ignored ROADMAP was amended with unchecked scoped additions P10/P12/P18/P24/P25/P26/P29; this tracked checkpoint preserves decisions for review.
- Lane ignored ROADMAP UX1/UX2/UX3/UX-R and tracked ADR docs/ADR-LANE-UX-DISRUPT-2026-11-04.md: commit 4a18d1d11853cf5e7f4b97c1175affd1f6208085.
- DIAG ignored ROADMAP P52 and P63–P65 already existed from 2026-11-02; tracked docs/continuous-intelligence.md commit 13c5cc4. Active DIAG writer owns Tier 2; do not amend while held.
- NFSU2 source worktree has unrelated WIP; do not reset or merge it. quakaII currently has a stale/reconciling writer lease; no bypass.
- Next action: owner project maintainers implement according to their existing phase/release gates. This checkpoint is NOT acceptance evidence. For actual close require targeted tests, independent phase gate, exact commit/rollback and production post-checks where applicable.

## Safety
Existing source-of-truth, security boundaries and fail-closed policy remain. AI never rewrites factual evidence. Lane laned remains sole routing writer; DIAG remains independently useful offline; MCPshka mutations require tool permissions, run IDs, audit, timeout and reconciliation.
