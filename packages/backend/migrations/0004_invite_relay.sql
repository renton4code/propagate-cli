-- Relay-encrypted scope key bundle for immediate-access PIN invites.

ALTER TABLE team_invites ADD COLUMN encrypted_scope_key_bundle jsonb;
ALTER TABLE team_invites ADD COLUMN relay_key_version integer;
