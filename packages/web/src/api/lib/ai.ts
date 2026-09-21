import { getCredential } from "./credentials";

export interface DeployPlan {
  runtime: "static" | "node" | "bun" | "python" | "docker" | "database" | "custom";
  installCommand: string | null;
  buildCommand: string | null;
  startCommand: string | null;
  port: number;
  dockerfile: string;
  envVars: string[];
  notes: string;
}

interface AiTarget {
  url: string;
  key: string;
  model: string;
  source: "openai" | "gateway";
}

async function resolveTarget(): Promise<AiTarget | null> {
  const creds = await getCredential("openai");
  if (creds?.apiKey) {
    return {
      url: "https://api.openai.com/v1/chat/completions",
      key: creds.apiKey,
      model: creds.model || "gpt-4o-mini",
      source: "openai",
    };
  }
  // Fallback so AI setup still works before the owner pastes their own key.
  if (process.env.AI_GATEWAY_BASE_URL && process.env.AI_GATEWAY_API_KEY) {
    return {
      url: `${process.env.AI_GATEWAY_BASE_URL.replace(/\/$/, "")}/chat/completions`,
      key: process.env.AI_GATEWAY_API_KEY,
      model: "gpt-4o-mini",
      source: "gateway",
    };
  }
  return null;
}

const SYSTEM_PROMPT = `You are the deploy planner for RedXAIHost, a self-hosting control panel.
You receive a project's file manifest plus key file contents, and you return how to run it on a
Linux worker node using Docker.

Rules:
- Pick the simplest correct runtime.
- Static sites must be served by nginx or a tiny static server on port 80 inside the container.
- Node/Bun/Python services must bind 0.0.0.0 and read PORT from the environment.
- The Dockerfile must be complete, multi-stage where it helps, and must not reference files that
  are absent from the manifest.
- envVars lists only the environment variable NAMES the app needs. Never invent secret values.
- notes: 2-4 short sentences for the owner, mentioning anything risky or missing.

Respond with JSON only, matching:
{"runtime":"static|node|bun|python|docker|database|custom","installCommand":string|null,
"buildCommand":string|null,"startCommand":string|null,"port":number,"dockerfile":string,
"envVars":string[],"notes":string}`;

export async function planDeployment(input: {
  projectName: string;
  runtimeHint?: string | null;
  manifest: string[];
  files: { path: string; content: string }[];
}): Promise<{ ok: boolean; plan?: DeployPlan; error?: string; source?: string }> {
  const target = await resolveTarget();
  if (!target) {
    return { ok: false, error: "No OpenAI key configured. Add one in Settings → Credentials." };
  }

  const fileBlocks = input.files
    .map((file) => `--- ${file.path} ---\n${file.content.slice(0, 6000)}`)
    .join("\n\n");

  const userPrompt = [
    `Project: ${input.projectName}`,
    input.runtimeHint ? `Owner's runtime hint: ${input.runtimeHint}` : null,
    `File manifest (${input.manifest.length} entries):`,
    input.manifest.slice(0, 400).join("\n"),
    "",
    "Key file contents:",
    fileBlocks || "(none readable)",
  ]
    .filter(Boolean)
    .join("\n");

  try {
    const res = await fetch(target.url, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${target.key}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        model: target.model,
        messages: [
          { role: "system", content: SYSTEM_PROMPT },
          { role: "user", content: userPrompt },
        ],
        response_format: { type: "json_object" },
        temperature: 0.1,
      }),
    });

    if (!res.ok) {
      const text = await res.text();
      return { ok: false, error: `AI request failed (${res.status}): ${text.slice(0, 300)}` };
    }

    const body = (await res.json()) as {
      choices?: { message?: { content?: string } }[];
    };
    const content = body.choices?.[0]?.message?.content;
    if (!content) return { ok: false, error: "AI returned an empty plan." };

    const parsed = JSON.parse(content) as Partial<DeployPlan>;
    if (!parsed.dockerfile) return { ok: false, error: "AI plan had no Dockerfile." };

    return {
      ok: true,
      source: target.source,
      plan: {
        runtime: (parsed.runtime as DeployPlan["runtime"]) ?? "custom",
        installCommand: parsed.installCommand ?? null,
        buildCommand: parsed.buildCommand ?? null,
        startCommand: parsed.startCommand ?? null,
        port: typeof parsed.port === "number" ? parsed.port : 8080,
        dockerfile: parsed.dockerfile,
        envVars: Array.isArray(parsed.envVars) ? parsed.envVars.slice(0, 40) : [],
        notes: parsed.notes ?? "",
      },
    };
  } catch (error) {
    return { ok: false, error: error instanceof Error ? error.message : "AI planning failed." };
  }
}
