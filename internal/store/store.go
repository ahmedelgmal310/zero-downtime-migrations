// Package store implements data access for the three app releases. Each
// release is a separate Store so the code that ships in an image is exactly
// the code for one step of the expand/contract sequence:
//
//	v1: reads and writes "name" only (baseline schema)
//	v2: writes name AND first_name/last_name (dual write), still reads "name"
//	v3: reads and writes first_name/last_name only; never touches "name"
//
// Queries always list their columns. A SELECT * or RETURNING * would change
// result type when a column is added or dropped, which breaks cached prepared
// statements in running pods ("cached plan must not change result type").
package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmedelgmal310/zero-downtime-migrations/internal/names"
)

// User is the API representation shared by every release.
type User struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
}

// Errors returned by stores.
var (
	ErrNotFound    = errors.New("user not found")
	ErrUnsupported = errors.New("not supported by this release")
)

// Store is the data access contract of one release.
type Store interface {
	Create(ctx context.Context, name string) (User, error)
	Get(ctx context.Context, id int64) (User, error)
	SearchByLastName(ctx context.Context, lastName string, limit int) ([]User, error)
	// RequiredColumns are the users columns this release reads or writes. A pod
	// stays unready until the schema has all of them.
	RequiredColumns() []string
}

// Versions lists the releases in order.
var Versions = []string{"v1", "v2", "v3"}

// New returns the Store for an app release.
func New(version string, pool *pgxpool.Pool) (Store, error) {
	switch version {
	case "v1":
		return v1{pool}, nil
	case "v2":
		return v2{pool}, nil
	case "v3":
		return v3{pool}, nil
	}
	return nil, fmt.Errorf("unknown app version %q (want one of %v)", version, Versions)
}

// MissingColumns returns the required columns that the users table lacks.
func MissingColumns(ctx context.Context, q Querier, required []string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'users'`)
	if err != nil {
		return nil, fmt.Errorf("list users columns: %w", err)
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list users columns: %w", err)
	}
	var missing []string
	for _, c := range required {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	return missing, nil
}

// Querier is the subset of pgx used for read-only helpers.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func ptr(s string) *string { return &s }

// v1: baseline, single "name" column.
type v1 struct{ pool *pgxpool.Pool }

func (v1) RequiredColumns() []string { return []string{"id", "name"} }

func (s v1) Create(ctx context.Context, name string) (User, error) {
	u := User{Name: name}
	err := s.pool.QueryRow(ctx, `INSERT INTO users (name) VALUES ($1) RETURNING id`, name).Scan(&u.ID)
	return u, err
}

func (s v1) Get(ctx context.Context, id int64) (User, error) {
	u := User{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&u.Name)
	return u, notFound(err)
}

func (v1) SearchByLastName(context.Context, string, int) ([]User, error) {
	return nil, ErrUnsupported
}

// v2: dual write, reads the old column (still the source of truth).
type v2 struct{ pool *pgxpool.Pool }

func (v2) RequiredColumns() []string { return []string{"id", "name", "first_name", "last_name"} }

func (s v2) Create(ctx context.Context, name string) (User, error) {
	first, last := names.Split(name)
	u := User{Name: name, FirstName: ptr(first), LastName: ptr(last)}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (name, first_name, last_name) VALUES ($1, $2, $3) RETURNING id`,
		name, first, last).Scan(&u.ID)
	return u, err
}

func (s v2) Get(ctx context.Context, id int64) (User, error) {
	u := User{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&u.Name)
	if err != nil {
		return u, notFound(err)
	}
	// Rows not yet backfilled have NULL split columns; derive them from the
	// source of truth instead of exposing a half-migrated row.
	first, last := names.Split(u.Name)
	u.FirstName, u.LastName = ptr(first), ptr(last)
	return u, nil
}

func (v2) SearchByLastName(context.Context, string, int) ([]User, error) {
	return nil, ErrUnsupported
}

// v3: reads and writes the new columns only.
type v3 struct{ pool *pgxpool.Pool }

func (v3) RequiredColumns() []string { return []string{"id", "first_name", "last_name"} }

func (s v3) Create(ctx context.Context, name string) (User, error) {
	first, last := names.Split(name)
	u := User{Name: name, FirstName: ptr(first), LastName: ptr(last)}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (first_name, last_name) VALUES ($1, $2) RETURNING id`,
		first, last).Scan(&u.ID)
	return u, err
}

func (s v3) Get(ctx context.Context, id int64) (User, error) {
	u := User{ID: id}
	var first, last string
	err := s.pool.QueryRow(ctx, `SELECT first_name, last_name FROM users WHERE id = $1`, id).Scan(&first, &last)
	if err != nil {
		return u, notFound(err)
	}
	u.Name, u.FirstName, u.LastName = names.Join(first, last), ptr(first), ptr(last)
	return u, nil
}

func (s v3) SearchByLastName(ctx context.Context, lastName string, limit int) ([]User, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, first_name, last_name FROM users WHERE last_name = $1 ORDER BY id LIMIT $2`,
		lastName, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		var first, last string
		if err := r.Scan(&u.ID, &first, &last); err != nil {
			return u, err
		}
		u.Name, u.FirstName, u.LastName = names.Join(first, last), ptr(first), ptr(last)
		return u, nil
	})
}
