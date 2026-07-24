package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// jsonMapStore is a thread-safe persistent map[string]string backed by a JSON file.
// Values are cached in memory after the first load; reads never hit disk more than once.
// Writes update both the in-memory cache and the file.
type jsonMapStore struct {
	mu       sync.Mutex
	filename string
	cache    map[string]string
	loaded   bool
}

// storeDir returns the base directory for bunker's JSON stores. It honors
// CLAUDE_BUNKER_STORE_DIR (used for test isolation and custom setups) and
// otherwise falls back to ~/.claude. Returns "" if no directory can be found,
// in which case the store operates in-memory only.
func storeDir() string {
	if d := os.Getenv("CLAUDE_BUNKER_STORE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// newJSONMapStore creates a store backed by <storeDir>/<filename>, resolved
// lazily so the directory can be overridden at runtime (e.g. in tests).
func newJSONMapStore(filename string) *jsonMapStore {
	return &jsonMapStore{filename: filename}
}

// path resolves the on-disk path, or "" for in-memory-only operation.
func (s *jsonMapStore) path() string {
	dir := storeDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, s.filename)
}

// ensureLoaded reads the JSON file into cache on the first call. Must be called with mu held.
func (s *jsonMapStore) ensureLoaded() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.cache = map[string]string{}
	p := s.path()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	// Ignore unmarshal errors — treat corrupt files as empty.
	_ = json.Unmarshal(data, &s.cache)
}

// persist writes the cache to disk. Must be called with mu held.
func (s *jsonMapStore) persist() error {
	p := s.path()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// Get returns the value for key, or empty string if not found.
func (s *jsonMapStore) Get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	return s.cache[key]
}

// Set stores a key-value pair and persists to disk.
// Setting an empty value deletes the key.
func (s *jsonMapStore) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	if value == "" {
		delete(s.cache, key)
	} else {
		s.cache[key] = value
	}
	return s.persist()
}

// All returns a copy of all key-value pairs.
func (s *jsonMapStore) All() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	cp := make(map[string]string, len(s.cache))
	for k, v := range s.cache {
		cp[k] = v
	}
	return cp
}

// Prune removes entries for which keep returns false. Persists only if entries were removed.
func (s *jsonMapStore) Prune(keep func(key string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureLoaded()
	changed := false
	for k := range s.cache {
		if !keep(k) {
			delete(s.cache, k)
			changed = true
		}
	}
	if changed {
		_ = s.persist()
	}
}
