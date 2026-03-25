package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"i8sl/internal/shortener"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	db *sql.DB
}

func NewStore(connString string) (*Store, error) {
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, fmt.Errorf("open postgres database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	store := &Store{db: db}
	if err := store.Ping(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres database: %w", err)
	}

	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) CreateRule(ctx context.Context, rule shortener.Rule) (shortener.Rule, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO rules (
			code,
			target_url,
			created_at,
			expires_at,
			max_usages,
			used_count,
			last_used_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		rule.Code,
		rule.URL,
		rule.CreatedAt.UTC(),
		nullableTime(rule.ExpiresAt),
		nullableInt(rule.MaxUsages),
		rule.UsedCount,
		nullableTime(rule.LastUsedAt),
	)
	if err != nil {
		if isUniqueError(err) {
			return shortener.Rule{}, shortener.ErrAlreadyExists
		}

		return shortener.Rule{}, fmt.Errorf("insert rule: %w", err)
	}

	return rule, nil
}

func (s *Store) GetRule(ctx context.Context, code string) (shortener.Rule, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at
		FROM rules
		WHERE code = $1
	`, code)

	rule, err := scanRule(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return shortener.Rule{}, shortener.ErrNotFound
		}

		return shortener.Rule{}, fmt.Errorf("get rule: %w", err)
	}

	return rule, nil
}

func (s *Store) DeleteRule(ctx context.Context, code string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE code = $1`, code)
	if err != nil {
		return fmt.Errorf("delete rule: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete rule rows affected: %w", err)
	}

	if rows == 0 {
		return shortener.ErrNotFound
	}

	return nil
}

func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM rules
		WHERE (expires_at IS NOT NULL AND expires_at <= $1)
		   OR (max_usages IS NOT NULL AND used_count >= max_usages)
	`, now.UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired rules: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired rules rows affected: %w", err)
	}

	return rows, nil
}

func (s *Store) ResolveRule(ctx context.Context, code string, now time.Time) (shortener.Rule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return shortener.Rule{}, fmt.Errorf("begin resolve transaction: %w", err)
	}

	defer func() {
		_ = tx.Rollback()
	}()

	row := tx.QueryRowContext(ctx, `
		SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at
		FROM rules
		WHERE code = $1
		FOR UPDATE
	`, code)

	rule, err := scanRule(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return shortener.Rule{}, shortener.ErrNotFound
		}

		return shortener.Rule{}, fmt.Errorf("load rule for resolve: %w", err)
	}

	if expired, reason := rule.Expired(now); expired {
		return shortener.Rule{}, &shortener.ExpiredError{Rule: rule, Reason: reason}
	}

	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE rules
		SET used_count = used_count + 1,
			last_used_at = $1
		WHERE code = $2
	`, now, code); err != nil {
		return shortener.Rule{}, fmt.Errorf("update rule usage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return shortener.Rule{}, fmt.Errorf("commit resolve transaction: %w", err)
	}

	rule.UsedCount++
	rule.LastUsedAt = &now

	return rule, nil
}

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS rules (
		code TEXT PRIMARY KEY,
		target_url TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		expires_at TIMESTAMPTZ NULL,
		max_usages BIGINT NULL,
		used_count BIGINT NOT NULL DEFAULT 0,
		last_used_at TIMESTAMPTZ NULL
	);
	CREATE INDEX IF NOT EXISTS idx_rules_expires_at ON rules(expires_at);
	`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate postgres schema: %w", err)
	}

	return nil
}

func scanRule(scan func(dest ...any) error) (shortener.Rule, error) {
	var (
		rule       shortener.Rule
		createdAt  time.Time
		expiresAt  sql.NullTime
		maxUsages  sql.NullInt64
		lastUsedAt sql.NullTime
	)

	err := scan(
		&rule.Code,
		&rule.URL,
		&createdAt,
		&expiresAt,
		&maxUsages,
		&rule.UsedCount,
		&lastUsedAt,
	)
	if err != nil {
		return shortener.Rule{}, err
	}

	rule.CreatedAt = createdAt.UTC()

	if expiresAt.Valid {
		expiresAtTime := expiresAt.Time.UTC()
		rule.ExpiresAt = &expiresAtTime
	}

	if maxUsages.Valid {
		value := maxUsages.Int64
		rule.MaxUsages = &value
	}

	if lastUsedAt.Valid {
		lastUsedAtTime := lastUsedAt.Time.UTC()
		rule.LastUsedAt = &lastUsedAtTime
	}

	return rule, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}

	return value.UTC()
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}

	return *value
}

func isUniqueError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}

	return false
}
