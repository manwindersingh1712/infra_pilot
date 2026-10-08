package db

// MigrateTo exposes migrateTo to the db_test integration tests.
var MigrateTo = migrateTo

// MigrationLockID exposes the migration advisory lock key, so tests can hold
// the lock from another connection.
const MigrationLockID = migrationLockID
