-- Preserve past consent records; new phone registrations do not record consent.
ALTER TABLE customers ALTER COLUMN consent_version DROP NOT NULL;
ALTER TABLE customers ALTER COLUMN consent_at DROP NOT NULL;
ALTER TABLE customers ALTER COLUMN consent_at DROP DEFAULT;
