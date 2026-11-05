ALTER TABLE bonus_transactions DROP CONSTRAINT bonus_transactions_type_check;
ALTER TABLE bonus_transactions ADD CONSTRAINT bonus_transactions_type_check CHECK(type IN ('cashback','redemption','referral','reversal','adjustment','award'));
ALTER TABLE bonus_transactions ADD COLUMN operation_key text UNIQUE;
-- Legacy settings remain for historic records; new flows use fixed rules.
UPDATE loyalty_settings SET cashback_percent=0,max_redemption_percent=50,referral_reward=500,referral_enabled=true,updated_at=now() WHERE id=1;
