ALTER TABLE user ADD COLUMN redx_account_id TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS user_redx_account_id_unique ON user(redx_account_id) WHERE redx_account_id IS NOT NULL;

ALTER TABLE nodes ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE projects ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE deployments ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE project_replicas ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE domains ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE credentials ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE usage_events ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE app_builds ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';
ALTER TABLE activity ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'owner';

CREATE INDEX IF NOT EXISTS nodes_tenant_idx ON nodes(tenant_id);
CREATE INDEX IF NOT EXISTS projects_tenant_idx ON projects(tenant_id);
CREATE INDEX IF NOT EXISTS deployments_tenant_idx ON deployments(tenant_id);
CREATE INDEX IF NOT EXISTS project_replicas_tenant_idx ON project_replicas(tenant_id);
CREATE INDEX IF NOT EXISTS domains_tenant_idx ON domains(tenant_id);
CREATE INDEX IF NOT EXISTS credentials_tenant_idx ON credentials(tenant_id);
CREATE INDEX IF NOT EXISTS usage_events_tenant_idx ON usage_events(tenant_id);
CREATE INDEX IF NOT EXISTS app_builds_tenant_idx ON app_builds(tenant_id);
CREATE INDEX IF NOT EXISTS activity_tenant_idx ON activity(tenant_id);
