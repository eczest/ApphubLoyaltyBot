package migrations

import _ "embed"

// Schema is the immutable initial migration. Add numbered migrations for future versions.
//
//go:embed 001_init.sql
var Schema string

//go:embed 002_simple_rewards.sql
var SimpleRewards string

//go:embed 003_registration_without_consent.sql
var RegistrationWithoutConsent string
