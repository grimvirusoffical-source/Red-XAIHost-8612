/**
 * Refuse to run without real secrets. Without them credentials would be
 * encrypted under a publicly known key and Better Auth would sign sessions
 * with its default secret. Imported first by the server entry so it runs
 * before any module derives keys.
 */
export const REQUIRED_SECRETS = ["BETTER_AUTH_SECRET", "CREDENTIAL_SECRET"] as const;

export function missingSecrets(env: Record<string, string | undefined> = process.env): string[] {
  return REQUIRED_SECRETS.filter((name) => !(env[name] ?? "").trim());
}

export function secretsHint(missing: string[]): string {
  return (
    `RedXAIHost will not start: ${missing.join(" and ")} ${missing.length > 1 ? "are" : "is"} empty.\n` +
    "Run `bun run setup:selfhost` from the repo root to generate them into .env, " +
    "or set them yourself (32+ random bytes each, e.g. `openssl rand -hex 32`)."
  );
}

export function assertSecrets(env: Record<string, string | undefined> = process.env): void {
  const missing = missingSecrets(env);
  if (missing.length) throw new Error(secretsHint(missing));
}
