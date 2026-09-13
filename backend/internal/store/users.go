package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	Email     string    `db:"email"`
	PassHash  string    `db:"pass_hash"`
	Img       *string   `db:"img"`
	Role      string    `db:"role"`
	Active    bool      `db:"active"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

const getUserQuery = `
	SELECT id, name, email, pass_hash, img, role, active, created_at, updated_at
	FROM users
	WHERE id = $1
`

func GetUserByID(ctx context.Context, pool *pgxpool.Pool, userId string) (User, error) {
	row, err := pool.Query(ctx, getUserQuery, userId)
	if err != nil {
		return User{}, classify(err)
	}

	user, err := pgx.CollectExactlyOneRow(row, pgx.RowToStructByName[User])
	if err != nil {
		return User{}, classify(err)
	}

	return user, nil
}
