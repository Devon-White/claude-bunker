package sessions

import (
	"sync"
	"testing"
)

// Two independent store instances over the SAME file, writing different keys
// concurrently, must not lose each other's writes.
func TestConcurrentStoresDoNotClobber(t *testing.T) {
	// TestMain points CLAUDE_BUNKER_STORE_DIR at a temp dir; both stores share it.
	a := newJSONMapStore("concurrent.json")
	b := newJSONMapStore("concurrent.json")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(n int) { defer wg.Done(); _ = a.Set("a"+itoa(n), "1") }(i)
		go func(n int) { defer wg.Done(); _ = b.Set("b"+itoa(n), "1") }(i)
	}
	wg.Wait()

	// A fresh reader must see all 100 keys.
	c := newJSONMapStore("concurrent.json")
	all := c.All()
	if len(all) != 100 {
		t.Fatalf("lost writes: want 100 keys, got %d", len(all))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
