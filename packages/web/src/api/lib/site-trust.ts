export type SiteTrustMode = "no" | "5days" | "20days" | "30days" | "indefinitely" | "custom";

export type SiteTrustChoice = {
  mode: SiteTrustMode;
  days?: number;
};

export type NormalizedSiteTrust = {
  mode: SiteTrustMode;
  remember: boolean;
  maxAgeSeconds: number | null;
};

const DAY = 86_400;
const FIXED_DAYS: Partial<Record<SiteTrustMode, number>> = {
  "5days": 5,
  "20days": 20,
  "30days": 30,
};

// Browser implementations may cap persistent cookies below this value. The
// server-side session remains revocable and intentionally long-lived.
const UNTIL_REVOKED_SECONDS = 2_147_483_647;

export function normalizeSiteTrust(value: unknown): NormalizedSiteTrust {
  const input = value && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {};
  const mode = typeof input.mode === "string" ? input.mode as SiteTrustMode : "30days";

  if (mode === "no") return { mode, remember: false, maxAgeSeconds: null };
  if (mode === "indefinitely") return { mode, remember: true, maxAgeSeconds: UNTIL_REVOKED_SECONDS };

  if (mode === "custom") {
    const days = Number(input.days);
    if (!Number.isInteger(days) || days < 1 || days > 3650) {
      throw new Error("Custom stay-signed-in duration must be 1 to 3650 days.");
    }
    return { mode, remember: true, maxAgeSeconds: days * DAY };
  }

  const days = FIXED_DAYS[mode];
  if (!days) throw new Error("Invalid stay-signed-in option.");
  return { mode, remember: true, maxAgeSeconds: days * DAY };
}
