-- +goose Up
-- Auto-generated (or user-renamed) display title for the chat sidebar.
ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE sessions DROP COLUMN IF EXISTS title;
