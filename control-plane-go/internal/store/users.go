package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const userCols = `"id", "email", "password", "name", "createdAt", "updatedAt"`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.CreatedAt.Time, &u.UpdatedAt.Time)
	return u, noRows(err)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM "User" WHERE "email" = $1`, email))
}

// CreateUser returns ErrConflict when the email is already registered.
func (s *Store) CreateUser(ctx context.Context, email, passwordHash string, name *string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO "User" ("id", "email", "password", "name") VALUES ($1, $2, $3, $4) RETURNING `+userCols,
		cuid.New(), email, passwordHash, name))
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	return u, err
}

// UpsertDevUser finds or creates dev@local for POST /dev/login.
func (s *Store) UpsertDevUser(ctx context.Context) (User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO "User" ("id", "email", "password") VALUES ($1, 'dev@local', 'dev-password-not-used')
		 ON CONFLICT ("email") DO UPDATE SET "email" = EXCLUDED."email"
		 RETURNING `+userCols, cuid.New()))
}
