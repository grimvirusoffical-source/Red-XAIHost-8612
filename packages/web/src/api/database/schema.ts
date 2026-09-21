import { sql } from "drizzle-orm";
import { index, integer, sqliteTable, text } from "drizzle-orm/sqlite-core";

export * from "./auth-schema";

const now = sql`(unixepoch())`;

/**
 * Compute nodes that actually run workloads: the owner's Windows PC and any
 * VPS added later. If no node is online, every project it hosts is reported
 * down — the control plane never serves traffic itself.
 */
export const nodes = sqliteTable(
  "nodes",
  {
    id: text("id").primaryKey(),
    name: text("name").notNull(),
    /** pc | vps */
    kind: text("kind").notNull().default("vps"),
    /** Bearer token the agent on that machine authenticates with (hashed). */
    tokenHash: text("token_hash").notNull(),
    tokenPreview: text("token_preview").notNull(),
    os: text("os"),
    arch: text("arch"),
    agentVersion: text("agent_version"),
    publicIp: text("public_ip"),
    /** pending | online | offline | disabled */
    status: text("status").notNull().default("pending"),
    cpuCores: integer("cpu_cores"),
    memoryMb: integer("memory_mb"),
    diskGb: integer("disk_gb"),
    cpuPercent: integer("cpu_percent"),
    memPercent: integer("mem_percent"),
    dockerAvailable: integer("docker_available", { mode: "boolean" }).notNull().default(false),
    cloudflaredAvailable: integer("cloudflared_available", { mode: "boolean" })
      .notNull()
      .default(false),
    labels: text("labels"),
    lastSeenAt: integer("last_seen_at"),
    createdAt: integer("created_at").notNull().default(now),
  },
  (t) => [index("nodes_status_idx").on(t.status)],
);

/** A hosted thing: site, API, container, python service, database, or app backend. */
export const projects = sqliteTable(
  "projects",
  {
    id: text("id").primaryKey(),
    name: text("name").notNull(),
    slug: text("slug").notNull().unique(),
    description: text("description"),
    /** static | node | bun | python | docker | database | custom */
    runtime: text("runtime").notNull().default("static"),
    /** upload | git */
    sourceKind: text("source_kind").notNull().default("upload"),
    gitUrl: text("git_url"),
    gitBranch: text("git_branch").default("main"),
    /** S3 key of the uploaded bundle. */
    bundleKey: text("bundle_key"),
    bundleName: text("bundle_name"),
    bundleSize: integer("bundle_size"),
    nodeId: text("node_id"),
    /** Port the container/process listens on inside the node. */
    port: integer("port"),
    buildCommand: text("build_command"),
    startCommand: text("start_command"),
    installCommand: text("install_command"),
    dockerfile: text("dockerfile"),
    envVars: text("env_vars"),
    aiNotes: text("ai_notes"),
    /** draft | deploying | running | stopped | failed | no_capacity */
    status: text("status").notNull().default("draft"),
    autoDeploy: integer("auto_deploy", { mode: "boolean" }).notNull().default(true),
    lastDeployedAt: integer("last_deployed_at"),
    createdAt: integer("created_at").notNull().default(now),
    updatedAt: integer("updated_at").notNull().default(now),
  },
  (t) => [index("projects_node_idx").on(t.nodeId), index("projects_status_idx").on(t.status)],
);

/** One deploy attempt, with the job the agent picks up and the log it writes back. */
export const deployments = sqliteTable(
  "deployments",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id").notNull(),
    nodeId: text("node_id"),
    /** queued | claimed | building | running | succeeded | failed | cancelled */
    status: text("status").notNull().default("queued"),
    /** deploy | stop | restart | remove */
    action: text("action").notNull().default("deploy"),
    trigger: text("trigger").notNull().default("manual"),
    commitSha: text("commit_sha"),
    logs: text("logs").notNull().default(""),
    error: text("error"),
    startedAt: integer("started_at"),
    finishedAt: integer("finished_at"),
    createdAt: integer("created_at").notNull().default(now),
  },
  (t) => [
    index("deployments_project_idx").on(t.projectId),
    index("deployments_status_idx").on(t.status),
  ],
);

/** Custom domains, registrar-side DNS state, and the Cloudflare tunnel fronting them. */
export const domains = sqliteTable(
  "domains",
  {
    id: text("id").primaryKey(),
    hostname: text("hostname").notNull().unique(),
    projectId: text("project_id"),
    /** godaddy | namecheap | cloudflare | manual */
    registrar: text("registrar").notNull().default("manual"),
    /** pending | dns_set | live | error */
    status: text("status").notNull().default("pending"),
    tunnelId: text("tunnel_id"),
    tunnelName: text("tunnel_name"),
    cnameTarget: text("cname_target"),
    zoneId: text("zone_id"),
    lastCheckedAt: integer("last_checked_at"),
    lastError: text("last_error"),
    createdAt: integer("created_at").notNull().default(now),
  },
  (t) => [index("domains_project_idx").on(t.projectId)],
);

/** Encrypted third-party credentials: OpenAI, Cloudflare, GoDaddy, Namecheap, GitHub, Expo. */
export const credentials = sqliteTable("credentials", {
  /** provider key, e.g. openai | cloudflare | godaddy | namecheap | github | expo */
  id: text("id").primaryKey(),
  label: text("label").notNull(),
  /** AES-256-GCM ciphertext blob of a JSON object. */
  secretEnc: text("secret_enc").notNull(),
  /** Last 4 chars of the main key, for display only. */
  hint: text("hint"),
  /** unknown | valid | invalid */
  verifyStatus: text("verify_status").notNull().default("unknown"),
  verifyMessage: text("verify_message"),
  verifiedAt: integer("verified_at"),
  updatedAt: integer("updated_at").notNull().default(now),
});

/** Per-project traffic rollups reported by the agent's reverse proxy. */
export const usageEvents = sqliteTable(
  "usage_events",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id").notNull(),
    nodeId: text("node_id"),
    /** Bucket start, unix seconds, rounded to the hour. */
    bucketAt: integer("bucket_at").notNull(),
    requests: integer("requests").notNull().default(0),
    uniqueVisitors: integer("unique_visitors").notNull().default(0),
    bytesOut: integer("bytes_out").notNull().default(0),
    errors: integer("errors").notNull().default(0),
    avgMs: integer("avg_ms").notNull().default(0),
    topPath: text("top_path"),
    topCountry: text("top_country"),
    createdAt: integer("created_at").notNull().default(now),
  },
  (t) => [
    index("usage_project_bucket_idx").on(t.projectId, t.bucketAt),
    index("usage_bucket_idx").on(t.bucketAt),
  ],
);

/** App binary builds dispatched to CI (Windows/Mac via GH Actions, iOS/Android via EAS). */
export const appBuilds = sqliteTable(
  "app_builds",
  {
    id: text("id").primaryKey(),
    projectId: text("project_id"),
    /** ios | android | windows | mac | linux */
    platform: text("platform").notNull(),
    /** queued | dispatched | building | succeeded | failed */
    status: text("status").notNull().default("queued"),
    /** github_actions | eas */
    provider: text("provider").notNull().default("github_actions"),
    workflow: text("workflow"),
    runUrl: text("run_url"),
    artifactUrl: text("artifact_url"),
    version: text("version"),
    notes: text("notes"),
    error: text("error"),
    createdAt: integer("created_at").notNull().default(now),
    finishedAt: integer("finished_at"),
  },
  (t) => [index("app_builds_platform_idx").on(t.platform)],
);

/** Everything that happened, for the activity feed. */
export const activity = sqliteTable(
  "activity",
  {
    id: text("id").primaryKey(),
    /** node | project | domain | deployment | build | auth | system */
    scope: text("scope").notNull(),
    refId: text("ref_id"),
    message: text("message").notNull(),
    /** info | warn | error | success */
    level: text("level").notNull().default("info"),
    meta: text("meta"),
    createdAt: integer("created_at").notNull().default(now),
  },
  (t) => [index("activity_created_idx").on(t.createdAt)],
);

export type Node = typeof nodes.$inferSelect;
export type Project = typeof projects.$inferSelect;
export type Deployment = typeof deployments.$inferSelect;
export type Domain = typeof domains.$inferSelect;
export type Credential = typeof credentials.$inferSelect;
export type UsageEvent = typeof usageEvents.$inferSelect;
export type AppBuild = typeof appBuilds.$inferSelect;
export type Activity = typeof activity.$inferSelect;
