CREATE TABLE `activity` (
	`id` text PRIMARY KEY NOT NULL,
	`scope` text NOT NULL,
	`ref_id` text,
	`message` text NOT NULL,
	`level` text DEFAULT 'info' NOT NULL,
	`meta` text,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `activity_created_idx` ON `activity` (`created_at`);--> statement-breakpoint
CREATE TABLE `app_builds` (
	`id` text PRIMARY KEY NOT NULL,
	`project_id` text,
	`platform` text NOT NULL,
	`status` text DEFAULT 'queued' NOT NULL,
	`provider` text DEFAULT 'github_actions' NOT NULL,
	`workflow` text,
	`run_url` text,
	`artifact_url` text,
	`version` text,
	`notes` text,
	`error` text,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL,
	`finished_at` integer
);
--> statement-breakpoint
CREATE INDEX `app_builds_platform_idx` ON `app_builds` (`platform`);--> statement-breakpoint
CREATE TABLE `credentials` (
	`id` text PRIMARY KEY NOT NULL,
	`label` text NOT NULL,
	`secret_enc` text NOT NULL,
	`hint` text,
	`verify_status` text DEFAULT 'unknown' NOT NULL,
	`verify_message` text,
	`verified_at` integer,
	`updated_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE TABLE `deployments` (
	`id` text PRIMARY KEY NOT NULL,
	`project_id` text NOT NULL,
	`node_id` text,
	`status` text DEFAULT 'queued' NOT NULL,
	`action` text DEFAULT 'deploy' NOT NULL,
	`trigger` text DEFAULT 'manual' NOT NULL,
	`commit_sha` text,
	`logs` text DEFAULT '' NOT NULL,
	`error` text,
	`started_at` integer,
	`finished_at` integer,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `deployments_project_idx` ON `deployments` (`project_id`);--> statement-breakpoint
CREATE INDEX `deployments_status_idx` ON `deployments` (`status`);--> statement-breakpoint
CREATE TABLE `domains` (
	`id` text PRIMARY KEY NOT NULL,
	`hostname` text NOT NULL,
	`project_id` text,
	`registrar` text DEFAULT 'manual' NOT NULL,
	`status` text DEFAULT 'pending' NOT NULL,
	`tunnel_id` text,
	`tunnel_name` text,
	`cname_target` text,
	`zone_id` text,
	`last_checked_at` integer,
	`last_error` text,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `domains_hostname_unique` ON `domains` (`hostname`);--> statement-breakpoint
CREATE INDEX `domains_project_idx` ON `domains` (`project_id`);--> statement-breakpoint
CREATE TABLE `nodes` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`kind` text DEFAULT 'vps' NOT NULL,
	`token_hash` text NOT NULL,
	`token_preview` text NOT NULL,
	`os` text,
	`arch` text,
	`agent_version` text,
	`public_ip` text,
	`status` text DEFAULT 'pending' NOT NULL,
	`cpu_cores` integer,
	`memory_mb` integer,
	`disk_gb` integer,
	`cpu_percent` integer,
	`mem_percent` integer,
	`docker_available` integer DEFAULT false NOT NULL,
	`cloudflared_available` integer DEFAULT false NOT NULL,
	`labels` text,
	`last_seen_at` integer,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `nodes_status_idx` ON `nodes` (`status`);--> statement-breakpoint
CREATE TABLE `project_replicas` (
	`id` text PRIMARY KEY NOT NULL,
	`project_id` text NOT NULL,
	`node_id` text NOT NULL,
	`status` text DEFAULT 'pending' NOT NULL,
	`last_deployment_id` text,
	`last_error` text,
	`last_deployed_at` integer,
	`updated_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `project_replicas_project_node_uniq` ON `project_replicas` (`project_id`,`node_id`);--> statement-breakpoint
CREATE INDEX `project_replicas_project_idx` ON `project_replicas` (`project_id`);--> statement-breakpoint
CREATE INDEX `project_replicas_node_idx` ON `project_replicas` (`node_id`);--> statement-breakpoint
CREATE INDEX `project_replicas_status_idx` ON `project_replicas` (`status`);--> statement-breakpoint
CREATE TABLE `projects` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`slug` text NOT NULL,
	`description` text,
	`runtime` text DEFAULT 'static' NOT NULL,
	`source_kind` text DEFAULT 'upload' NOT NULL,
	`git_url` text,
	`git_branch` text DEFAULT 'main',
	`bundle_key` text,
	`bundle_name` text,
	`bundle_size` integer,
	`node_id` text,
	`replicate_everywhere` integer DEFAULT true NOT NULL,
	`port` integer,
	`build_command` text,
	`start_command` text,
	`install_command` text,
	`dockerfile` text,
	`env_vars` text,
	`ai_notes` text,
	`status` text DEFAULT 'draft' NOT NULL,
	`auto_deploy` integer DEFAULT true NOT NULL,
	`last_deployed_at` integer,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL,
	`updated_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `projects_slug_unique` ON `projects` (`slug`);--> statement-breakpoint
CREATE INDEX `projects_node_idx` ON `projects` (`node_id`);--> statement-breakpoint
CREATE INDEX `projects_status_idx` ON `projects` (`status`);--> statement-breakpoint
CREATE TABLE `usage_events` (
	`id` text PRIMARY KEY NOT NULL,
	`project_id` text NOT NULL,
	`node_id` text,
	`bucket_at` integer NOT NULL,
	`requests` integer DEFAULT 0 NOT NULL,
	`unique_visitors` integer DEFAULT 0 NOT NULL,
	`bytes_out` integer DEFAULT 0 NOT NULL,
	`errors` integer DEFAULT 0 NOT NULL,
	`avg_ms` integer DEFAULT 0 NOT NULL,
	`top_path` text,
	`top_country` text,
	`created_at` integer DEFAULT (unixepoch()) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `usage_project_bucket_idx` ON `usage_events` (`project_id`,`bucket_at`);--> statement-breakpoint
CREATE INDEX `usage_bucket_idx` ON `usage_events` (`bucket_at`);--> statement-breakpoint
CREATE TABLE `account` (
	`id` text PRIMARY KEY NOT NULL,
	`account_id` text NOT NULL,
	`provider_id` text NOT NULL,
	`user_id` text NOT NULL,
	`access_token` text,
	`refresh_token` text,
	`id_token` text,
	`access_token_expires_at` integer,
	`refresh_token_expires_at` integer,
	`scope` text,
	`password` text,
	`created_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
	`updated_at` integer NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE INDEX `account_userId_idx` ON `account` (`user_id`);--> statement-breakpoint
CREATE TABLE `session` (
	`id` text PRIMARY KEY NOT NULL,
	`expires_at` integer NOT NULL,
	`token` text NOT NULL,
	`created_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
	`updated_at` integer NOT NULL,
	`ip_address` text,
	`user_agent` text,
	`user_id` text NOT NULL,
	FOREIGN KEY (`user_id`) REFERENCES `user`(`id`) ON UPDATE no action ON DELETE cascade
);
--> statement-breakpoint
CREATE UNIQUE INDEX `session_token_unique` ON `session` (`token`);--> statement-breakpoint
CREATE INDEX `session_userId_idx` ON `session` (`user_id`);--> statement-breakpoint
CREATE TABLE `user` (
	`id` text PRIMARY KEY NOT NULL,
	`name` text NOT NULL,
	`email` text NOT NULL,
	`email_verified` integer DEFAULT false NOT NULL,
	`image` text,
	`created_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
	`updated_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL
);
--> statement-breakpoint
CREATE UNIQUE INDEX `user_email_unique` ON `user` (`email`);--> statement-breakpoint
CREATE TABLE `verification` (
	`id` text PRIMARY KEY NOT NULL,
	`identifier` text NOT NULL,
	`value` text NOT NULL,
	`expires_at` integer NOT NULL,
	`created_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL,
	`updated_at` integer DEFAULT (cast(unixepoch('subsecond') * 1000 as integer)) NOT NULL
);
--> statement-breakpoint
CREATE INDEX `verification_identifier_idx` ON `verification` (`identifier`);