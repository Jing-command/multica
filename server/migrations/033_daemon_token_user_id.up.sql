DELETE FROM daemon_token;
ALTER TABLE daemon_token ADD COLUMN user_id UUID NOT NULL REFERENCES "user"(id);
