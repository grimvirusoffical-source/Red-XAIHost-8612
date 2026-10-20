// Runs before every other server import (ES modules evaluate in import order).
import { missingSecrets, secretsHint } from "./api/lib/secrets-guard";

const missing = missingSecrets();
if (missing.length) {
  console.error(secretsHint(missing));
  process.exit(1);
}
