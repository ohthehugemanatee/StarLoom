-- +migrate Up

ALTER TABLE star_charts ADD COLUMN child_member_id INTEGER REFERENCES family_members(id) ON DELETE SET NULL;
CREATE INDEX idx_star_charts_child_member_id ON star_charts(child_member_id);

-- +migrate Down

DROP INDEX IF EXISTS idx_star_charts_child_member_id;
ALTER TABLE star_charts DROP COLUMN child_member_id;
