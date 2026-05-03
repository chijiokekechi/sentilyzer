package cache_test

import (
	"testing"
	"time"

	"github.com/chijiokekechi/sentilyzer/api/internal/cache"
)

func TestTTLCache_HitMissExpire(t *testing.T) {
	c, err := cache.New[string, int](4, time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.Set("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("Get(a) = (%d, %v), want (1, true)", v, ok)
	}
	if _, ok := c.Get("missing"); ok {
		t.Error("Get(missing) returned ok=true")
	}
}

func TestTTLCache_TTLExpiry(t *testing.T) {
	c, err := cache.New[string, int](4, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.Set("a", 1)
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get("a"); ok {
		t.Error("expected expired entry to miss")
	}
}
