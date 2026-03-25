package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"i8sl/internal/shortener"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func NewStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure sqlite busy_timeout: %w", err)
	}

	store := &Store{db: db}
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
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		rule.Code,
		rule.URL,
		formatTime(rule.CreatedAt),
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
		WHERE code = ?
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
	result, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE code = ?`, code)
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

func (s *Store) ResolveRule(ctx context.Context, code string, now time.Time) (shortener.Rule, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return shortener.Rule{}, fmt.Errorf("begin resolve transaction: %w", err)
	}

	defer tx.Rollback()

	row := tx.QueryRowContext(ctx, `
		SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at
		FROM rules
		WHERE code = ?
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
			last_used_at = ?
		WHERE code = ?
	`, formatTime(now), code); err != nil {
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
		created_at TEXT NOT NULL,
		expires_at TEXT NULL,
		max_usages INTEGER NULL,
		used_count INTEGER NOT NULL DEFAULT 0,
		last_used_at TEXT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_rules_expires_at ON rules(expires_at);
	`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}

	return nil
}

func scanRule(scan func(dest ...any) error) (shortener.Rule, error) {
	var (
		rule       shortener.Rule
		createdAt  string
		expiresAt  sql.NullString
		maxUsages  sql.NullInt64
		lastUsedAt sql.NullString
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

	parsedCreatedAt, err := parseTime(createdAt)
	if err != nil {
		return shortener.Rule{}, fmt.Errorf("parse created_at: %w", err)
	}
	rule.CreatedAt = parsedCreatedAt

	if expiresAt.Valid {
		parsedExpiresAt, err := parseTime(expiresAt.String)
		if err != nil {
			return shortener.Rule{}, fmt.Errorf("parse expires_at: %w", err)
		}
		rule.ExpiresAt = &parsedExpiresAt
	}

	if maxUsages.Valid {
		value := maxUsages.Int64
		rule.MaxUsages = &value
	}

	if lastUsedAt.Valid {
		parsedLastUsedAt, err := parseTime(lastUsedAt.String)
		if err != nil {
			return shortener.Rule{}, fmt.Errorf("parse last_used_at: %w", err)
		}
		rule.LastUsedAt = &parsedLastUsedAt
	}

	return rule, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}

	return formatTime(*value)
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}

	return *value
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}

	return parsed.UTC(), nil
}

func isUniqueError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") || strings.Contains(message, "constraint failed")
}
