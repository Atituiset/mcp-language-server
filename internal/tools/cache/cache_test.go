package cache

import (
	"testing"
	"time"
)

func TestDeleteByFile(t *testing.T) {
	c := NewSearchResultCache(time.Minute)
	c.SetWithFiles("a", 1, 0, []string{"/x/f1.c", "/x/f2.c"})
	c.SetWithFiles("b", 2, 0, []string{"/x/f3.c"})
	c.Set("c", 3, 0) // no dependency info -> conservative delete

	c.DeleteByFile("/x/f1.c")

	if _, ok := c.Get("a"); ok {
		t.Error("entry a depends on f1.c and should be deleted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("entry b does not depend on f1.c and should survive")
	}
	if _, ok := c.Get("c"); ok {
		t.Error("entry without dependency info should be conservatively deleted")
	}
}

// Replacing a key must move its reverse-index entries to the new dependency
// set — the stale file must no longer invalidate it.
func TestSetWithFilesReindexOnReplace(t *testing.T) {
	c := NewSearchResultCache(time.Minute)
	c.SetWithFiles("a", 1, 0, []string{"/x/old.c"})
	c.SetWithFiles("a", 2, 0, []string{"/x/new.c"})

	c.DeleteByFile("/x/old.c")
	if v, ok := c.Get("a"); !ok || v != 2 {
		t.Error("replaced entry must not be invalidated by its previous dependency")
	}

	c.DeleteByFile("/x/new.c")
	if _, ok := c.Get("a"); ok {
		t.Error("entry must be invalidated by its current dependency")
	}
}

// Expired entries must be swept on writes so the map cannot grow unbounded
// (Cleanup was previously never called).
func TestExpiredEntriesSweptOnSet(t *testing.T) {
	c := NewCache(0) // no default TTL
	c.Set("stale", 1, 20*time.Millisecond)
	if c.Size() != 1 {
		t.Fatalf("expected 1 item, got %d", c.Size())
	}
	time.Sleep(30 * time.Millisecond)

	c.Set("fresh", 2, 0) // triggers the sweep
	if c.Size() != 1 {
		t.Errorf("expected expired entry to be swept, size=%d", c.Size())
	}
	if _, ok := c.Get("stale"); ok {
		t.Error("expired entry must not be returned")
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Error("fresh entry must survive the sweep")
	}
}

// Delete must also drop the reverse-index entries, otherwise a later
// DeleteByFile touches keys that no longer exist (harmless but wasteful).
func TestDeleteKeepsIndexConsistent(t *testing.T) {
	c := NewSearchResultCache(time.Minute)
	c.SetWithFiles("a", 1, 0, []string{"/x/f1.c"})
	c.SetWithFiles("b", 2, 0, []string{"/x/f1.c"})
	c.Delete("a")

	c.DeleteByFile("/x/f1.c")
	if _, ok := c.Get("b"); ok {
		t.Error("b should still be invalidated by f1.c")
	}
	if c.Size() != 0 {
		t.Errorf("expected empty cache, size=%d", c.Size())
	}
}
