# Red-XAI Agent Forge

Portable skill source for designing and coordinating specialized GPT-style agent teams.

## Install
Place this directory in a skill-capable agent environment and register `SKILL.md` according to that environment's skill-loading mechanism.

## Important
This package defines orchestration behavior; it does not itself create a new model runtime or bypass platform tool permissions. Real parallel agents require an agent/task runtime that exposes spawning/delegation APIs.

See `examples/red-xai-team.md` for a starter team.
