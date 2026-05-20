-- Server-side pending join requests: add 'pending' and 'declined' status,
-- plus columns to store what the joiner requested.

ALTER TABLE members DROP CONSTRAINT IF EXISTS members_status_check;
ALTER TABLE members ADD CONSTRAINT members_status_check CHECK (status IN ('active', 'revoked', 'replaced', 'pending', 'declined'));

ALTER TABLE members ADD COLUMN IF NOT EXISTS requested_role text;
ALTER TABLE members ADD COLUMN IF NOT EXISTS requested_management boolean NOT NULL DEFAULT false;
ALTER TABLE members ADD COLUMN IF NOT EXISTS requested_scopes jsonb;
ALTER TABLE members ADD COLUMN IF NOT EXISTS declined_by_key_sha text;
ALTER TABLE members ADD COLUMN IF NOT EXISTS declined_at timestamptz;
