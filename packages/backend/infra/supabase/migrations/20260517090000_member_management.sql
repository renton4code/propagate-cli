-- Add config-management access as a member bit separate from scope access.

alter table members
  add column if not exists management boolean not null default false;

alter table team_invites
  add column if not exists requested_management boolean not null default false;

update members
set management = true
where role = 'admins';
