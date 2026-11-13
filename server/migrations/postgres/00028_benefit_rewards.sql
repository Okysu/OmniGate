-- +goose Up
ALTER TABLE benefit_campaigns DROP CONSTRAINT benefit_campaigns_reward_type_check;
ALTER TABLE benefit_campaigns ADD CONSTRAINT benefit_campaigns_reward_type_check CHECK(reward_type IN ('balance','subscription','reset_card','lottery','bundle'));
ALTER TABLE benefit_campaigns ADD COLUMN reward TEXT NOT NULL DEFAULT 'null';
ALTER TABLE benefit_campaigns ADD COLUMN rewards TEXT NOT NULL DEFAULT '[]';
ALTER TABLE benefit_claims ADD COLUMN reset_card_batch_id uuid REFERENCES reset_card_batches(id);
ALTER TABLE benefit_claims ADD COLUMN lottery_chances INTEGER NOT NULL DEFAULT 0 CHECK(lottery_chances BETWEEN 0 AND 1000);
ALTER TABLE benefit_claims ADD COLUMN reward_results TEXT NOT NULL DEFAULT '[]';
-- +goose Down
ALTER TABLE benefit_claims DROP COLUMN reward_results;
-- New reward claims must be retained; rollback requires removing the feature's data explicitly.
ALTER TABLE benefit_claims DROP COLUMN lottery_chances;
ALTER TABLE benefit_claims DROP COLUMN reset_card_batch_id;
ALTER TABLE benefit_campaigns DROP COLUMN rewards;
ALTER TABLE benefit_campaigns DROP COLUMN reward;
ALTER TABLE benefit_campaigns DROP CONSTRAINT benefit_campaigns_reward_type_check;
ALTER TABLE benefit_campaigns ADD CONSTRAINT benefit_campaigns_reward_type_check CHECK(reward_type IN ('balance','subscription'));
