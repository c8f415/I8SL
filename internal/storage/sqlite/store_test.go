package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"i8sl/internal/shortener"
)

func TestDeleteExpiredRemovesExpiredRules(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "i8sl.db"))
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Minute)
	maxUsages := int64(1)

	_, err = store.CreateRule(context.Background(), shortener.Rule{
		Code:      "ttl01",
		URL:       "https://example.com/ttl",
		CreatedAt: now.Add(-time.Hour),
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatalf("create ttl rule: %v", err)
	}

	_, err = store.CreateRule(context.Background(), shortener.Rule{
		Code:      "use01",
		URL:       "https://example.com/use",
		CreatedAt: now.Add(-time.Hour),
		MaxUsages: &maxUsages,
		UsedCount: 1,
	})
	if err != nil {
		t.Fatalf("create usage-limited rule: %v", err)
	}

	_, err = store.CreateRule(context.Background(), shortener.Rule{
		Code:      "keep01",
		URL:       "https://example.com/keep",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("create active rule: %v", err)
	}

	deleted, err := store.DeleteExpired(context.Background(), now)
	if err != nil {
		t.Fatalf("delete expired rules: %v", err)
	}

	if deleted != 2 {
		t.Fatalf("expected 2 deleted rules, got %d", deleted)
	}

	if _, err := store.GetRule(context.Background(), "keep01"); err != nil {
		t.Fatalf("expected keep01 to remain, got %v", err)
	}
}
