-- Propagate backend schema for team setup and encrypted secret metadata.
-- The CLI never sends plaintext env values or plaintext scope keys to this schema.

create table if not exists teams (
  id text primary key,
  name text not null,
  current_config_revision integer not null default 1,
  created_by_key_sha text not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  archived_at timestamptz,
  constraint teams_current_config_revision_positive check (current_config_revision > 0)
);

create table if not exists team_config_revisions (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  revision_number integer not null,
  config_hash text not null,
  config_snapshot jsonb not null,
  pushed_by_key_sha text not null,
  pushed_at timestamptz not null default now(),
  operation_id text not null,
  request_fingerprint text,
  constraint team_config_revisions_revision_positive check (revision_number > 0),
  constraint team_config_revisions_hash_format check (config_hash like 'sha256:%'),
  constraint team_config_revisions_fingerprint_format check (request_fingerprint is null or request_fingerprint like 'sha256:%')
);

create unique index if not exists team_config_revisions_team_revision_idx
  on team_config_revisions(team_id, revision_number);

create unique index if not exists team_config_revisions_team_operation_idx
  on team_config_revisions(team_id, operation_id);

create table if not exists setup_operations (
  operation_id text primary key,
  request_fingerprint text not null,
  team_id text not null references teams(id) on delete cascade,
  created_at timestamptz not null default now(),
  constraint setup_operations_fingerprint_format check (request_fingerprint like 'sha256:%')
);

create table if not exists members (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  handle text not null,
  public_key_sha text not null,
  signing_public_key text not null,
  encryption_public_key text not null,
  role text not null,
  management boolean not null default false,
  status text not null default 'active',
  approved_by_key_sha text,
  approved_at timestamptz,
  revoked_by_key_sha text,
  revoked_at timestamptz,
  created_at timestamptz not null default now(),
  constraint members_role_check check (role in ('admins', 'developers')),
  constraint members_status_check check (status in ('active', 'revoked', 'replaced')),
  constraint members_public_key_sha_format check (public_key_sha like 'sha256:%'),
  constraint members_signing_public_key_format check (signing_public_key like 'ssh-ed25519 %'),
  constraint members_encryption_public_key_format check (encryption_public_key like 'x25519:%')
);

create unique index if not exists members_team_public_key_sha_idx
  on members(team_id, public_key_sha);

create unique index if not exists members_team_signing_public_key_active_idx
  on members(team_id, signing_public_key)
  where status = 'active';

create table if not exists scopes (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  name text not null,
  kind text not null default 'custom',
  created_at timestamptz not null default now(),
  archived_at timestamptz,
  constraint scopes_kind_check check (kind in ('builtin', 'custom')),
  constraint scopes_name_check check (name ~ '^[a-z][a-z0-9_-]*$')
);

create unique index if not exists scopes_team_name_idx
  on scopes(team_id, name);

create table if not exists env_file_mappings (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  scope_id bigint not null references scopes(id) on delete cascade,
  path text not null,
  config_revision integer not null,
  active boolean not null default true,
  created_at timestamptz not null default now(),
  constraint env_file_mappings_relative_path check (
    path <> ''
    and path not like '/%'
    and path not like '../%'
    and path <> '..'
    and path not like '%/../%'
  ),
  constraint env_file_mappings_config_revision_positive check (config_revision > 0)
);

create index if not exists env_file_mappings_team_scope_active_idx
  on env_file_mappings(team_id, scope_id, active);

create table if not exists scope_access_rules (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  scope_id bigint not null references scopes(id) on delete cascade,
  subject_type text not null,
  subject_value text not null,
  permission text not null,
  config_revision integer not null,
  active boolean not null default true,
  created_at timestamptz not null default now(),
  constraint scope_access_rules_subject_type_check check (subject_type in ('role', 'member')),
  constraint scope_access_rules_permission_check check (permission in ('none', 'read', 'write', 'admin')),
  constraint scope_access_rules_config_revision_positive check (config_revision > 0)
);

create index if not exists scope_access_rules_lookup_idx
  on scope_access_rules(team_id, scope_id, subject_type, subject_value, active);

create table if not exists scope_key_envelopes (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  scope_id bigint not null references scopes(id) on delete cascade,
  recipient_key_sha text not null,
  scope_key_version integer not null,
  encrypted_scope_key text not null,
  algorithm text not null,
  created_by_key_sha text not null,
  config_revision integer not null,
  created_at timestamptz not null default now(),
  revoked_at timestamptz,
  constraint scope_key_envelopes_recipient_key_sha_format check (recipient_key_sha like 'sha256:%'),
  constraint scope_key_envelopes_version_positive check (scope_key_version > 0),
  constraint scope_key_envelopes_config_revision_positive check (config_revision > 0)
);

create index if not exists scope_key_envelopes_active_lookup_idx
  on scope_key_envelopes(team_id, scope_id, recipient_key_sha, scope_key_version, revoked_at);

create table if not exists secret_variables (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  scope_id bigint not null references scopes(id) on delete cascade,
  env_file_path text not null,
  name text not null,
  current_version_id bigint,
  deleted_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint secret_variables_name_check check (name ~ '^[A-Za-z_][A-Za-z0-9_]*$'),
  constraint secret_variables_relative_path check (
    env_file_path <> ''
    and env_file_path not like '/%'
    and env_file_path not like '../%'
    and env_file_path <> '..'
    and env_file_path not like '%/../%'
  )
);

create unique index if not exists secret_variables_scope_file_name_idx
  on secret_variables(team_id, scope_id, env_file_path, name);

create index if not exists secret_variables_pull_idx
  on secret_variables(team_id, scope_id, deleted_at);

create table if not exists secret_versions (
  id bigserial primary key,
  variable_id bigint not null references secret_variables(id) on delete cascade,
  version_number integer not null,
  ciphertext text not null,
  nonce text not null,
  algorithm text not null,
  scope_key_version integer not null,
  plaintext_fingerprint text,
  created_by_key_sha text not null,
  created_at timestamptz not null default now(),
  operation_id text not null,
  constraint secret_versions_version_positive check (version_number > 0),
  constraint secret_versions_scope_key_version_positive check (scope_key_version > 0),
  constraint secret_versions_created_by_key_sha_format check (created_by_key_sha like 'sha256:%'),
  constraint secret_versions_no_raw_fingerprint check (plaintext_fingerprint is null or plaintext_fingerprint like 'keyed:%')
);

create unique index if not exists secret_versions_variable_version_idx
  on secret_versions(variable_id, version_number);

create unique index if not exists secret_versions_variable_operation_idx
  on secret_versions(variable_id, operation_id);

create table if not exists env_push_operations (
  team_id text not null references teams(id) on delete cascade,
  operation_id text not null,
  request_fingerprint text not null,
  result jsonb not null,
  created_at timestamptz not null default now(),
  primary key (team_id, operation_id),
  constraint env_push_operations_fingerprint_format check (request_fingerprint like 'sha256:%')
);

do $$
begin
  if not exists (
    select 1
    from pg_constraint
    where conname = 'secret_variables_current_version_fk'
  ) then
    alter table secret_variables
      add constraint secret_variables_current_version_fk
      foreign key (current_version_id) references secret_versions(id)
      deferrable initially deferred;
  end if;
end $$;

create table if not exists audit_events (
  id bigserial primary key,
  team_id text not null references teams(id) on delete cascade,
  actor_key_sha text not null,
  actor_handle text not null,
  event_type text not null,
  scope_id bigint references scopes(id) on delete set null,
  target_key_sha text,
  config_revision integer,
  metadata jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  constraint audit_events_actor_key_sha_format check (actor_key_sha like 'sha256:%'),
  constraint audit_events_target_key_sha_format check (target_key_sha is null or target_key_sha like 'sha256:%')
);

create index if not exists audit_events_team_created_idx
  on audit_events(team_id, created_at);

create index if not exists audit_events_team_actor_created_idx
  on audit_events(team_id, actor_key_sha, created_at);

create index if not exists audit_events_team_type_created_idx
  on audit_events(team_id, event_type, created_at);

create table if not exists request_nonces (
  public_key_sha text not null,
  nonce text not null,
  expires_at timestamptz not null,
  first_seen_at timestamptz not null default now(),
  primary key (public_key_sha, nonce),
  constraint request_nonces_public_key_sha_format check (public_key_sha like 'sha256:%')
);

create index if not exists request_nonces_expires_at_idx
  on request_nonces(expires_at);
