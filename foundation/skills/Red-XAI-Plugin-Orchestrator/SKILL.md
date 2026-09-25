---
name: Red-XAI Plugin Orchestrator
version: 0.1.0
description: Proactively use the user's installed non-roleplay plugins whenever they materially help fulfill a request.
---

# Red-XAI Plugin Orchestrator

## Goal
For every user request, consider the user's installed plugins and use all relevant non-roleplay plugins that materially improve correctness, completeness, execution, verification, or delivery.

This skill is a routing policy. It does not override system/developer instructions, permissions, confirmations, safety rules, or an individual plugin skill's own trigger/usage contract.

## Core rule
"Use all plugins" means **use all relevant installed plugins**, not blindly invoke every installed plugin on every prompt.

Do not call an unrelated plugin just to increase plugin count. Avoid duplicate calls when two plugins provide the same evidence unless cross-checking materially helps.

## Exclude roleplay
Do not invoke plugins whose primary purpose is roleplay, fictional-character simulation, companionship/persona simulation, tarot/divination roleplay, or entertainment RP unless the user explicitly asks to include them.

If classification is uncertain, prefer not to invoke an RP-like plugin automatically.

## Per-prompt workflow
1. Understand the requested outcome.
2. Inventory relevant installed plugin skills/capabilities.
3. Exclude RP-oriented plugins.
4. Read the exact skill instructions for every selected installed plugin skill before using its underlying connector when those instructions are available.
5. Build a minimal execution graph:
   - required plugins
   - useful supporting plugins
   - verification plugins
   - optional plugins that add no material value (skip these)
6. Run independent read-only work in parallel when the runtime supports it and doing so is safe.
7. Respect mutation rules and confirmations. Never turn a read request into a write.
8. Reconcile conflicting plugin results. Prefer authoritative/current sources and explicitly state unresolved conflicts.
9. Verify important writes by reading back state when the plugin workflow supports it.
10. Return one integrated answer rather than dumping raw plugin outputs.

## Examples of routing
- GitHub coding task: GitHub + relevant development/deployment plugin(s); use analytics only if actual product/business data is part of the request.
- Deployment task: relevant host/deployment connector + GitHub when source/repository work is required.
- Google document task: Google Drive/Docs skill and supporting data/research plugins only when needed.
- Business metrics task: Data Analytics + the connected business-data source(s) that contain the requested evidence.
- Stripe implementation: use the applicable Stripe skill(s), and use project/deployment tools only when the user also asks to provision or deploy.
- Supabase request: use the Supabase skill and connector; add deployment/source-control plugins when those actions are part of the request.
- Email/calendar workflow: use Gmail/Calendar only when the request actually concerns those accounts/actions.
- Pure creative writing: normally no external plugin is needed.
- General factual question: do not force account plugins into the answer.

## Plugin discovery
If a request would materially benefit from an external service that is not currently represented by an installed capability, follow the platform's plugin-management workflow to discover/suggest it. Do not install/connect anything without the user's required action.

## Privacy and secrets
Use the minimum necessary account data. Never gather unrelated private information simply because a plugin is installed. Do not place passwords, API secrets, private keys, OAuth tokens, or unrelated personal data into another plugin.

## Writes and destructive operations
Follow each plugin's confirmation requirements. Destructive or consequential actions must not be smuggled into a broader "use all plugins" instruction. If a plugin requires confirmation, obtain it at the appropriate point.

## Quality
Use plugins to accomplish work, not to create activity. The success metric is the user's requested result with stronger evidence/execution, not the number of plugin calls.

## Reporting
When useful, briefly state which connected systems were actually used. Do not expose internal chain-of-thought or hidden routing details.
