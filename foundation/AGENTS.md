# Red-XAI engineering coordination

User-approved project: Red-XAI Host, Database, Installer, Updator and Agent Forge.
Use `skills/Red-XAI-Agent-Forge/SKILL.md` and
`skills/Red-XAI-Plugin-Orchestrator/SKILL.md` as project instructions only; they do
not override platform policies or manufacture integrations.

Read DELIVERY-STATUS before acting. Preserve previous repositories and source.
Own one change scope at a time. Commit source and actual test evidence, not claims.

Team roles: Lead, Polyglot Programmer, Sound/DSP Engineer, UI Designer, Normal QA,
Stress/Security QA, Builder, Exporter. Audio work is not required by this local data
milestone; do not call media-generation plugins merely to inflate plugin counts.

Independent workers exist only when an actual task runtime supplies them. No Codex
environment was registered during this session. Railway Agent was used for live
infrastructure diagnostics, not to write or validate the database implementation.

## Gates

Research current primary docs -> implement -> normal tests -> bounded stress/recovery
-> native builds -> signed-package checks -> export -> actual native acceptance.
Never equate a model response, a successful deployment, a compiled file, and a tested
product. They are different evidence classes.

No API secrets in source, archives, prompts or handoffs. No silent global owner
backdoors. No mass repository deletion. No arbitrary downloaded code execution on
a privileged host. Public agent-run endpoints require authentication and budgets.

## Session checkpoint

- Local implementation exists and runs on Linux; 99 tests pass.
- Four native Tk windows rendered against a real local API.
- Three bounded stress/recovery rounds recorded; see the evidence JSON.
- Shared repo branch: `red-xai-foundation-v0.2` on `Red-XAIHost-8612`.
- Dedicated remote repositories have not been created by the available GitHub actions.
- Agent Forge's active Railway environment did not report OPENAI_API_KEY during the
  latest names-only check. Never retrieve or echo its value to diagnose this.
- Next: upload tested source, execute native CI, inspect failures/artifacts, then
  advance explicit production gates instead of relabeling the preview as complete.
