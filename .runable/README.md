# Managed App Releases

`releases.json` is the source of truth for app-impacting template releases. The latest entry's
version is also written to `.template-version`.

The derived files (`.template-version` and `.runable/protected-files.json`) come from
`releases.json` and the `__` files. The generator lives outside this tree, in
`runable/packages/sandbox/app-template/`. It is platform tooling, not an app script, and not part of
the app's `bun run lint`. The image build fails on stale output, so run
`bun run --filter @runable/sandbox build:app-template` from the monorepo root after changing a `__`
file or adding a release.

At scaffold time the platform rewrites the `__` files and re-stamps `protected-files.json` from its
current template, so both can be newer than `.template-version`, which keeps the version baked into
the box's image. Template migrations must leave `__` files alone.

Add a release entry in the same commit as the template changes:

```json
{
  "version": "0.1.1",
  "policy": "optional"
}
```

Only add fields when the template diff is insufficient or shared checks need an override:

```json
{
  "version": "0.1.2",
  "policy": "required",
  "migration_notes": ["Preserve customized Expo plugins."],
  "verification_commands": ["bun install", "bun run typecheck", "bun run build"],
  "runtime_checks": ["The web preview starts without startup errors."]
}
```

Runable derives `revision` and `released_at` from the commit where the version first appears. The
explicit values on `0.1.0` only seed the historical baseline and must not be added to new releases.
