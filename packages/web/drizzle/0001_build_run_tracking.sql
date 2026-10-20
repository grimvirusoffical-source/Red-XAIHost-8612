ALTER TABLE `app_builds` ADD `run_id` integer;--> statement-breakpoint
ALTER TABLE `app_builds` ADD `head_sha` text;--> statement-breakpoint
ALTER TABLE `app_builds` ADD `dispatched_at` integer;