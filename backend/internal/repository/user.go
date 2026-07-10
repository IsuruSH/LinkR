package repository

import (
	"context"
	"fmt"

	"github.com/IsuruSh/linkr/internal/db"
	"github.com/IsuruSh/linkr/internal/domain"
)

type UserRepository struct {
	q *db.Queries
}

func NewUserRepository(dbtx db.DBTX) *UserRepository {
	return &UserRepository{q: db.New(dbtx)}
}

func toDomainUser(u db.User) domain.User {
	return domain.User{
		ID:           u.ID,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		CreatedAt:    u.CreatedAt,
	}
}

// Create inserts a user. Two concurrent registrations for the same email both
// reach the INSERT; the unique index rejects one. Checking "does this email
// exist" first would look correct and still lose the race.
func (r *UserRepository) Create(ctx context.Context, email, passwordHash string) (domain.User, error) {
	row, err := r.q.CreateUser(ctx, db.CreateUserParams{
		Email:        email,
		PasswordHash: passwordHash,
	})
	switch {
	case isUniqueViolation(err, constraintEmailUnique):
		return domain.User{}, domain.ErrEmailTaken
	case err != nil:
		return domain.User{}, fmt.Errorf("creating user: %w", err)
	}
	return toDomainUser(row), nil
}

// GetByEmail is case-insensitive because the column is citext.
//
// A missing user returns ErrInvalidCredentials rather than a "not found":
// the login handler must not distinguish "no such account" from "wrong
// password", or the endpoint becomes a user-enumeration oracle.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (domain.User, error) {
	row, err := r.q.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.User{}, notFound(err, domain.ErrInvalidCredentials)
	}
	return toDomainUser(row), nil
}
