# Red-XAI Plugin Orchestrator

A portable ChatGPT skill source that tells an agent to proactively consider all installed plugins on every prompt and invoke every **relevant non-roleplay** plugin.

It intentionally does not call every plugin blindly. Doing that would leak unnecessary data, create irrelevant side effects, increase latency, and conflict with individual plugin rules.

## Files
- `SKILL.md` — installable skill instructions.
- `templates/plugin-routing-checklist.md` — reusable routing checklist.

## Installation
Install/register this folder using the skill mechanism supported by your ChatGPT/agent environment.

The skill cannot override higher-priority platform instructions or grant plugin permissions that the user has not connected/authorized.
