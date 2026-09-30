import { client } from "./__client";

const schemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS "user" (
  "id" text PRIMARY KEY NOT NULL,
  "name" text NOT NULL,
  "email" text NOT NULL UNIQUE,
  "email_verified" integer DEFAULT 0 NOT NULL,
  "image" text,
  "created_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
  "updated_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL
);
CREATE TABLE IF NOT EXISTS "session" (
  "id" text PRIMARY KEY NOT NULL,
  "expires_at" integer NOT NULL,
  "token" text NOT NULL UNIQUE,
  "created_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
  "updated_at" integer NOT NULL,
  "ip_address" text,
  "user_agent" text,
  "user_id" text NOT NULL REFERENCES "user"("id") ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS "session_userId_idx" ON "session" ("user_id");
CREATE TABLE IF NOT EXISTS "account" (
  "id" text PRIMARY KEY NOT NULL,
  "account_id" text NOT NULL,
  "provider_id" text NOT NULL,
  "user_id" text NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
  "access_token" text,
  "refresh_token" text,
  "id_token" text,
  "access_token_expires_at" integer,
  "refresh_token_expires_at" integer,
  "scope" text,
  "password" text,
  "created_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
  "updated_at" integer NOT NULL
);
CREATE INDEX IF NOT EXISTS "account_userId_idx" ON "account" ("user_id");
CREATE TABLE IF NOT EXISTS "verification" (
  "id" text PRIMARY KEY NOT NULL,
  "identifier" text NOT NULL,
  "value" text NOT NULL,
  "expires_at" integer NOT NULL,
  "created_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
  "updated_at" integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL
);
CREATE INDEX IF NOT EXISTS "verification_identifier_idx" ON "verification" ("identifier");

CREATE TABLE IF NOT EXISTS "nodes" (
  "id" text PRIMARY KEY NOT NULL,
  "name" text NOT NULL,
  "kind" text DEFAULT 'vps' NOT NULL,
  "token_hash" text NOT NULL,
  "token_preview" text NOT NULL,
  "os" text,
  "arch" text,
  "agent_version" text,
  "public_ip" text,
  "status" text DEFAULT 'pending' NOT NULL,
  "cpu_cores" integer,
  "memory_mb" integer,
  "disk_gb" integer,
  "cpu_percent" integer,
  "mem_percent" integer,
  "docker_available" integer DEFAULT 0 NOT NULL,
  "cloudflared_available" integer DEFAULT 0 NOT NULL,
  "labels" text,
  "last_seen_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "nodes_status_idx" ON "nodes" ("status");

CREATE TABLE IF NOT EXISTS "projects" (
  "id" text PRIMARY KEY NOT NULL,
  "name" text NOT NULL,
  "slug" text NOT NULL UNIQUE,
  "description" text,
  "runtime" text DEFAULT 'static' NOT NULL,
  "source_kind" text DEFAULT 'upload' NOT NULL,
  "git_url" text,
  "git_branch" text DEFAULT 'main',
  "bundle_key" text,
  "bundle_name" text,
  "bundle_size" integer,
  "node_id" text,
  "replicate_everywhere" integer DEFAULT 1 NOT NULL,
  "port" integer,
  "build_command" text,
  "start_command" text,
  "install_command" text,
  "dockerfile" text,
  "env_vars" text,
  "ai_notes" text,
  "status" text DEFAULT 'draft' NOT NULL,
  "auto_deploy" integer DEFAULT 1 NOT NULL,
  "last_deployed_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL,
  "updated_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "projects_node_idx" ON "projects" ("node_id");
CREATE INDEX IF NOT EXISTS "projects_status_idx" ON "projects" ("status");

CREATE TABLE IF NOT EXISTS "deployments" (
  "id" text PRIMARY KEY NOT NULL,
  "project_id" text NOT NULL,
  "node_id" text,
  "status" text DEFAULT 'queued' NOT NULL,
  "action" text DEFAULT 'deploy' NOT NULL,
  "trigger" text DEFAULT 'manual' NOT NULL,
  "commit_sha" text,
  "logs" text DEFAULT '' NOT NULL,
  "error" text,
  "started_at" integer,
  "finished_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "deployments_project_idx" ON "deployments" ("project_id");
CREATE INDEX IF NOT EXISTS "deployments_status_idx" ON "deployments" ("status");

CREATE TABLE IF NOT EXISTS "project_replicas" (
  "id" text PRIMARY KEY NOT NULL,
  "project_id" text NOT NULL,
  "node_id" text NOT NULL,
  "status" text DEFAULT 'pending' NOT NULL,
  "last_deployment_id" text,
  "last_error" text,
  "last_deployed_at" integer,
  "updated_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS "project_replicas_project_node_uniq" ON "project_replicas" ("project_id","node_id");
CREATE INDEX IF NOT EXISTS "project_replicas_project_idx" ON "project_replicas" ("project_id");
CREATE INDEX IF NOT EXISTS "project_replicas_node_idx" ON "project_replicas" ("node_id");
CREATE INDEX IF NOT EXISTS "project_replicas_status_idx" ON "project_replicas" ("status");

CREATE TABLE IF NOT EXISTS "domains" (
  "id" text PRIMARY KEY NOT NULL,
  "hostname" text NOT NULL UNIQUE,
  "project_id" text,
  "registrar" text DEFAULT 'manual' NOT NULL,
  "status" text DEFAULT 'pending' NOT NULL,
  "tunnel_id" text,
  "tunnel_name" text,
  "cname_target" text,
  "zone_id" text,
  "last_checked_at" integer,
  "last_error" text,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "domains_project_idx" ON "domains" ("project_id");

CREATE TABLE IF NOT EXISTS "credentials" (
  "id" text PRIMARY KEY NOT NULL,
  "label" text NOT NULL,
  "secret_enc" text NOT NULL,
  "hint" text,
  "verify_status" text DEFAULT 'unknown' NOT NULL,
  "verify_message" text,
  "verified_at" integer,
  "updated_at" integer DEFAULT (unixepoch()) NOT NULL
);

CREATE TABLE IF NOT EXISTS "usage_events" (
  "id" text PRIMARY KEY NOT NULL,
  "project_id" text NOT NULL,
  "node_id" text,
  "bucket_at" integer NOT NULL,
  "requests" integer DEFAULT 0 NOT NULL,
  "unique_visitors" integer DEFAULT 0 NOT NULL,
  "bytes_out" integer DEFAULT 0 NOT NULL,
  "errors" integer DEFAULT 0 NOT NULL,
  "avg_ms" integer DEFAULT 0 NOT NULL,
  "top_path" text,
  "top_country" text,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "usage_project_bucket_idx" ON "usage_events" ("project_id","bucket_at");
CREATE INDEX IF NOT EXISTS "usage_bucket_idx" ON "usage_events" ("bucket_at");

CREATE TABLE IF NOT EXISTS "app_builds" (
  "id" text PRIMARY KEY NOT NULL,
  "project_id" text,
  "platform" text NOT NULL,
  "status" text DEFAULT 'queued' NOT NULL,
  "provider" text DEFAULT 'github_actions' NOT NULL,
  "workflow" text,
  "run_url" text,
  "artifact_url" text,
  "version" text,
  "notes" text,
  "error" text,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL,
  "finished_at" integer
);
CREATE INDEX IF NOT EXISTS "app_builds_platform_idx" ON "app_builds" ("platform");

CREATE TABLE IF NOT EXISTS "activity" (
  "id" text PRIMARY KEY NOT NULL,
  "scope" text NOT NULL,
  "ref_id" text,
  "message" text NOT NULL,
  "level" text DEFAULT 'info' NOT NULL,
  "meta" text,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "activity_created_idx" ON "activity" ("created_at");
CREATE TABLE IF NOT EXISTS "twoFactor" (
  "id" text PRIMARY KEY NOT NULL,
  "user_id" text NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
  "secret" text NOT NULL,
  "backup_codes" text NOT NULL,
  "verified" integer DEFAULT 0 NOT NULL,
  "failed_verification_count" integer DEFAULT 0 NOT NULL,
  "locked_until" integer
);
CREATE INDEX IF NOT EXISTS "two_factor_user_idx" ON "twoFactor" ("user_id");

CREATE TABLE IF NOT EXISTS "redx_databases" (
  "id" text PRIMARY KEY NOT NULL,
  "owner_user_id" text NOT NULL,
  "name" text NOT NULL,
  "source" text NOT NULL,
  "revision" integer DEFAULT 1 NOT NULL,
  "deleted_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL,
  "updated_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS "redx_databases_owner_name_uniq" ON "redx_databases" ("owner_user_id","name");
CREATE INDEX IF NOT EXISTS "redx_databases_owner_idx" ON "redx_databases" ("owner_user_id");

CREATE TABLE IF NOT EXISTS "redx_api_keys" (
  "id" text PRIMARY KEY NOT NULL,
  "database_id" text NOT NULL,
  "owner_user_id" text NOT NULL,
  "name" text NOT NULL,
  "key_hash" text NOT NULL UNIQUE,
  "key_preview" text NOT NULL,
  "scopes" text DEFAULT 'read' NOT NULL,
  "expires_at" integer,
  "last_used_at" integer,
  "revoked_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "redx_api_keys_database_idx" ON "redx_api_keys" ("database_id");
CREATE INDEX IF NOT EXISTS "redx_api_keys_owner_idx" ON "redx_api_keys" ("owner_user_id");

CREATE TABLE IF NOT EXISTS "redx_access_tokens" (
  "id" text PRIMARY KEY NOT NULL,
  "database_id" text NOT NULL,
  "owner_user_id" text NOT NULL,
  "name" text NOT NULL,
  "token_id" integer NOT NULL,
  "box_path" text,
  "token_hash" text NOT NULL UNIQUE,
  "token_preview" text NOT NULL,
  "scopes" text DEFAULT 'read' NOT NULL,
  "expires_at" integer,
  "last_used_at" integer,
  "revoked_at" integer,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS "redx_access_token_id_uniq" ON "redx_access_tokens" ("database_id","token_id");
CREATE INDEX IF NOT EXISTS "redx_access_tokens_database_idx" ON "redx_access_tokens" ("database_id");

CREATE TABLE IF NOT EXISTS "redx_audit" (
  "id" text PRIMARY KEY NOT NULL,
  "user_id" text,
  "database_id" text,
  "action" text NOT NULL,
  "ip" text,
  "user_agent" text,
  "meta" text,
  "created_at" integer DEFAULT (unixepoch()) NOT NULL
);
CREATE INDEX IF NOT EXISTS "redx_audit_database_idx" ON "redx_audit" ("database_id");
CREATE INDEX IF NOT EXISTS "redx_audit_user_idx" ON "redx_audit" ("user_id");
CREATE INDEX IF NOT EXISTS "redx_audit_created_idx" ON "redx_audit" ("created_at");

`;

async function hasColumn(table: string, column: string): Promise<boolean> {
  const safeTable = table.replaceAll('"', '""');
  const result = await client.execute('PRAGMA table_info("' + safeTable + '")');
  return result.rows.some((row) => String(row.name) === column);
}

async function ensureColumn(table: string, column: string, definition: string): Promise<void> {
  if (await hasColumn(table, column)) return;
  const safeTable = table.replaceAll('"', '""');
  const safeColumn = column.replaceAll('"', '""');
  await client.execute('ALTER TABLE "' + safeTable + '" ADD COLUMN "' + safeColumn + '" ' + definition);
}

export async function ensureDatabaseSchema(): Promise<void> {
  await client.executeMultiple(schemaSQL);
  await ensureColumn("user", "username", "text");
  await ensureColumn("user", "display_username", "text");
  await ensureColumn("user", "dob", "text");
  await ensureColumn("user", "terms_accepted_at", "integer");
  await ensureColumn("user", "role", "text NOT NULL DEFAULT 'user'");
  await ensureColumn("user", "two_factor_enabled", "integer DEFAULT 0");
  await client.execute('CREATE UNIQUE INDEX IF NOT EXISTS "user_username_uniq" ON "user" ("username") WHERE "username" IS NOT NULL');
}
