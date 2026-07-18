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
