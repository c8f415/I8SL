package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"

	"i8sl/internal/shortener"
)

func TestCreateRuleSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	ttl := now.Add(time.Hour)
	maxUsages := int64(5)
	rule := shortener.Rule{
		Code:      "demo42",
		URL:       "https://example.com/postgres",
		CreatedAt: now,
		ExpiresAt: &ttl,
		MaxUsages: &maxUsages,
	}

	mock.ExpectExec(`INSERT INTO rules`).
		WithArgs(rule.Code, rule.URL, rule.CreatedAt.UTC(), rule.ExpiresAt.UTC(), maxUsages, int64(0), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	created, err := store.CreateRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	if created.Code != rule.Code {
		t.Fatalf("expected code %q, got %q", rule.Code, created.Code)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestCreateRuleUniqueConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	rule := shortener.Rule{
		Code:      "alias01",
		URL:       "https://example.com/postgres",
		CreatedAt: time.Now().UTC(),
	}

	mock.ExpectExec(`INSERT INTO rules`).
		WillReturnError(&pgconn.PgError{Code: "23505"})

	_, err = store.CreateRule(context.Background(), rule)
	if !errors.Is(err, shortener.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestGetRuleSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	ttl := now.Add(30 * time.Minute)
	lastUsed := now.Add(5 * time.Minute)

	rows := sqlmock.NewRows([]string{"code", "target_url", "created_at", "expires_at", "max_usages", "used_count", "last_used_at"}).
		AddRow("demo42", "https://example.com/postgres", now, ttl, int64(7), int64(2), lastUsed)

	mock.ExpectQuery(`SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at`).
		WithArgs("demo42").
		WillReturnRows(rows)

	rule, err := store.GetRule(context.Background(), "demo42")
	if err != nil {
		t.Fatalf("get rule: %v", err)
	}

	if rule.UsedCount != 2 {
		t.Fatalf("expected used_count 2, got %d", rule.UsedCount)
	}

	if rule.LastUsedAt == nil || !rule.LastUsedAt.Equal(lastUsed) {
		t.Fatalf("expected last_used_at %v, got %v", lastUsed, rule.LastUsedAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDeleteRuleNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}

	mock.ExpectExec(`DELETE FROM rules WHERE code = \$1`).
		WithArgs("missing").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = store.DeleteRule(context.Background(), "missing")
	if !errors.Is(err, shortener.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveRuleUpdatesUsage(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{"code", "target_url", "created_at", "expires_at", "max_usages", "used_count", "last_used_at"}).
		AddRow("demo42", "https://example.com/postgres", now, sql.NullTime{}, sql.NullInt64{}, int64(0), sql.NullTime{})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at`).
		WithArgs("demo42").
		WillReturnRows(rows)
	mock.ExpectExec(`UPDATE rules`).
		WithArgs(now, "demo42").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	rule, err := store.ResolveRule(context.Background(), "demo42", now)
	if err != nil {
		t.Fatalf("resolve rule: %v", err)
	}

	if rule.UsedCount != 1 {
		t.Fatalf("expected used_count 1, got %d", rule.UsedCount)
	}

	if rule.LastUsedAt == nil || !rule.LastUsedAt.Equal(now) {
		t.Fatalf("expected last_used_at %v, got %v", now, rule.LastUsedAt)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestResolveRuleReturnsExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	maxUsages := int64(1)

	rows := sqlmock.NewRows([]string{"code", "target_url", "created_at", "expires_at", "max_usages", "used_count", "last_used_at"}).
		AddRow("demo42", "https://example.com/postgres", now, sql.NullTime{}, maxUsages, int64(1), sql.NullTime{})

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT code, target_url, created_at, expires_at, max_usages, used_count, last_used_at`).
		WithArgs("demo42").
		WillReturnRows(rows)
	mock.ExpectRollback()

	_, err = store.ResolveRule(context.Background(), "demo42", now)
	var expiredErr *shortener.ExpiredError
	if !errors.As(err, &expiredErr) {
		t.Fatalf("expected ExpiredError, got %v", err)
	}

	if expiredErr.Reason != "max_usages" {
		t.Fatalf("expected max_usages reason, got %q", expiredErr.Reason)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestDeleteExpired(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sqlmock: %v", err)
	}
	defer db.Close()

	store := &Store{db: db}
	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec(`DELETE FROM rules`).
		WithArgs(now).
		WillReturnResult(sqlmock.NewResult(0, 3))

	deleted, err := store.DeleteExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}

	if deleted != 3 {
		t.Fatalf("expected 3 deleted rows, got %d", deleted)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
