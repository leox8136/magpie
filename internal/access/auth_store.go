package access

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// authStore is immutable. Opening the file on each lookup checks permissions;
// identity, size, mtime and ctime detect replacement, edits and revocation.
// On systems without ctime, bytes are read again rather than trusting mtime.
type authStore struct {
	path   string
	info   os.FileInfo
	change time.Time
	digest [32]byte
	keys   map[[32]byte]Key
	lan    bool
}

var authSnapshot atomic.Pointer[authStore]
var authLoad sync.Mutex

// Prepared before replacement; readers still verify the bytes on disk before
// using it. A failed write or an external edit cannot publish these permissions.
var authPrepared atomic.Pointer[authStore]

func indexedStore(path string, b []byte, keys []Key) *authStore {
	// Marshal replaces invalid UTF-8 with U+FFFD. Re-read those rare
	// inputs so prepared permissions have exactly the persisted string values.
	if bytes.Contains(b, []byte(`\ufffd`)) || bytes.Contains(b, []byte("\ufffd")) {
		var persisted []Key
		if json.Unmarshal(b, &persisted) == nil {
			keys = persisted
		}
	}
	s := &authStore{path: path, digest: sha256.Sum256(b), keys: make(map[[32]byte]Key, len(keys))}
	for _, k := range keys {
		hash := sha256.Sum256([]byte(k.Secret))
		if _, exists := s.keys[hash]; !exists {
			id := keyIdentity(k)
			k.Limit, k.More, k.Models, k.Accounts, k.Concurrency = id.Limit, id.More, id.Models, id.Accounts, id.Concurrency
			s.keys[hash] = k
		}
		s.lan = s.lan || k.LAN
	}
	return s
}

func (s *authStore) matches(path string, info os.FileInfo) bool {
	return s != nil && s.path == path && s.info != nil &&
		os.SameFile(s.info, info) && s.info.Size() == info.Size() &&
		s.info.ModTime().Equal(info.ModTime()) && s.info.Mode() == info.Mode() &&
		!s.change.IsZero() && s.change.Equal(keyChangeTime(info))
}

func authenticationStore() (*authStore, error) {
	path := Path()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &authStore{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("key store is not a regular file")
	}
	if s := authSnapshot.Load(); s.matches(path, info) {
		return s, nil
	}
	authLoad.Lock()
	defer authLoad.Unlock()
	// Re-stat after waiting: a concurrent writer may have changed this file.
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if s := authSnapshot.Load(); s.matches(path, info) {
		return s, nil
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) || !keyChangeTime(info).Equal(keyChangeTime(after)) {
		return nil, errors.New("key store changed while being read")
	}
	digest := sha256.Sum256(b)
	s := &authStore{path: path, info: after, change: keyChangeTime(after), digest: digest}
	old := authSnapshot.Load()
	prepared := authPrepared.Load()
	if prepared != nil && prepared.path == path && prepared.digest == digest {
		old = prepared
	}
	if old != nil && old.path == path && old.digest == digest {
		s.keys, s.lan = old.keys, old.lan
	} else {
		var keys []Key
		if err := json.Unmarshal(b, &keys); err != nil {
			return nil, err
		}
		indexed := indexedStore(path, b, keys)
		s.keys, s.lan = indexed.keys, indexed.lan
	}

	authSnapshot.Store(s)
	return s, nil
}
