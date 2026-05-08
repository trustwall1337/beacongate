package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Store is the persistence interface used by the admin API. The first
// implementation is a JSON file; a database-backed store can be plugged in
// later behind the same contract.
type Store interface {
	List() []Rule
	Get(id string) (Rule, bool)
	Upsert(r Rule) error
	Delete(id string) (bool, error)
	Replace(rules []Rule) error
}

// FileStore is a JSON-backed Store. All mutations write the full file under
// a mutex to keep operator changes auditable and atomic.
type FileStore struct {
	path  string
	mu    sync.Mutex
	rules map[string]Rule
}

func OpenFileStore(path string) (*FileStore, error) {
	fs := &FileStore{path: path, rules: map[string]Rule{}}
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

func (fs *FileStore) load() error {
	data, err := os.ReadFile(fs.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return fmt.Errorf("policy store: %w", err)
	}
	for _, r := range rules {
		fs.rules[r.ID] = r
	}
	return nil
}

func (fs *FileStore) flushLocked() error {
	rules := make([]Rule, 0, len(fs.rules))
	for _, r := range fs.rules {
		rules = append(rules, r)
	}
	data, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}

func (fs *FileStore) List() []Rule {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	out := make([]Rule, 0, len(fs.rules))
	for _, r := range fs.rules {
		out = append(out, r)
	}
	return out
}

func (fs *FileStore) Get(id string) (Rule, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	r, ok := fs.rules[id]
	return r, ok
}

func (fs *FileStore) Upsert(r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.rules[r.ID] = r
	return fs.flushLocked()
}

func (fs *FileStore) Delete(id string) (bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.rules[id]; !ok {
		return false, nil
	}
	delete(fs.rules, id)
	return true, fs.flushLocked()
}

func (fs *FileStore) Replace(rules []Rule) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.rules = map[string]Rule{}
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
		fs.rules[r.ID] = r
	}
	return fs.flushLocked()
}
