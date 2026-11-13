-- +goose Up
CREATE TABLE benefit_campaigns_new (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),
 reward_type TEXT NOT NULL CHECK(reward_type IN ('balance','subscription','reset_card','lottery','bundle')),
 plan_id TEXT REFERENCES plans(id), amount_nano INTEGER NOT NULL CHECK(amount_nano>=0),
 price_nano INTEGER NOT NULL CHECK(price_nano>=0), stock INTEGER NOT NULL DEFAULT -1 CHECK(stock>=-1),
 per_user_limit INTEGER NOT NULL DEFAULT 1 CHECK(per_user_limit>=1),
 starts_at TEXT, ends_at TEXT, conditions TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, reward TEXT NOT NULL DEFAULT 'null', rewards TEXT NOT NULL DEFAULT '[]'
);

INSERT INTO benefit_campaigns_new SELECT *, 'null', '[]' FROM benefit_campaigns;
DROP TABLE benefit_campaigns;
ALTER TABLE benefit_campaigns_new RENAME TO benefit_campaigns;
ALTER TABLE benefit_claims ADD COLUMN reset_card_batch_id TEXT REFERENCES reset_card_batches(id);
ALTER TABLE benefit_claims ADD COLUMN lottery_chances INTEGER NOT NULL DEFAULT 0 CHECK(lottery_chances BETWEEN 0 AND 1000);
ALTER TABLE benefit_claims ADD COLUMN reward_results TEXT NOT NULL DEFAULT '[]';
-- +goose Down
ALTER TABLE benefit_claims DROP COLUMN reward_results;
CREATE TABLE benefit_campaigns_old (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),
 reward_type TEXT NOT NULL CHECK(reward_type IN ('balance','subscription')),
 plan_id TEXT REFERENCES plans(id), amount_nano INTEGER NOT NULL CHECK(amount_nano>=0),
 price_nano INTEGER NOT NULL CHECK(price_nano>=0), stock INTEGER NOT NULL DEFAULT -1 CHECK(stock>=-1),
 per_user_limit INTEGER NOT NULL DEFAULT 1 CHECK(per_user_limit>=1),
 starts_at TEXT, ends_at TEXT, conditions TEXT NOT NULL,
 version INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);

INSERT INTO benefit_campaigns_old SELECT id,title,description,status,reward_type,plan_id,amount_nano,price_nano,stock,per_user_limit,starts_at,ends_at,conditions,version,created_at,updated_at FROM benefit_campaigns;
DROP TABLE benefit_campaigns;
ALTER TABLE benefit_campaigns_old RENAME TO benefit_campaigns;
ALTER TABLE benefit_claims DROP COLUMN lottery_chances;
ALTER TABLE benefit_claims DROP COLUMN reset_card_batch_id;
