-- Remove legacy role concept. Access is now governed by management bool + per-member scopes.

-- Drop role-based access rules (subject_type = 'role'); only member-level rules remain.
DELETE FROM scope_access_rules WHERE subject_type = 'role';
ALTER TABLE scope_access_rules DROP CONSTRAINT IF EXISTS scope_access_rules_subject_type_check;
ALTER TABLE scope_access_rules ADD CONSTRAINT scope_access_rules_subject_type_check CHECK (subject_type = 'member');

-- Drop role column from members (management bool is the canonical source).
ALTER TABLE members DROP CONSTRAINT IF EXISTS members_role_check;
ALTER TABLE members DROP COLUMN IF EXISTS role;
ALTER TABLE members DROP COLUMN IF EXISTS requested_role;

-- Drop requested_role from team_invites.
ALTER TABLE team_invites DROP COLUMN IF EXISTS requested_role;
