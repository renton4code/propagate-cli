-- PIN-backed team invites (join-by-invite-code flow).

create table if not exists team_invites (
  id text primary key,
  team_id text not null references teams(id) on delete cascade,
  label text not null,
  pin_verifier text not null,
  status text not null,
  failed_pin_attempts integer not null default 0,
  requested_role text,
  requested_management boolean not null default false,
  requested_scopes jsonb,
  created_by_key_sha text not null,
  created_at timestamptz not null default now(),
  redeemed_at timestamptz,
  redeemed_by_key_sha text,
  constraint team_invites_status_check check (status in ('active', 'redeemed', 'revoked', 'invalidated_pin')),
  constraint team_invites_failed_attempts_non_negative check (failed_pin_attempts >= 0)
);

create index if not exists team_invites_team_status_idx
  on team_invites(team_id, status);
