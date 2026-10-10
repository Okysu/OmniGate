package affinity

import (
	"container/list"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Store holds session bindings: an LRU (capacity given per write, so a changed
// max_entries applies at once) whose entries expire after their TTL; the TTL
// slides — every hit and every rebind extends it.
type Store struct {
	mu  sync.Mutex
	ll  *list.List // front = most recently used
	m   map[[32]byte]*list.Element
	now func() time.Time
}

type binding struct {
	key     [32]byte
	channel uuid.UUID
	rule    string
	ttl     time.Duration
	expires time.Time
}

// NewStore creates an empty store; now is the clock (nil = time.Now).
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{ll: list.New(), m: map[[32]byte]*list.Element{}, now: now}
}

// Get returns the channel bound to key and refreshes the entry (sliding TTL,
// most recently used). Expired entries are removed.
func (s *Store) Get(key [32]byte) (uuid.UUID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.m[key]
	if !ok {
		return uuid.Nil, false
	}
	b := el.Value.(*binding)
	now := s.now()
	if !now.Before(b.expires) {
		s.remove(el)
		return uuid.Nil, false
	}
	b.expires = now.Add(b.ttl)
	s.ll.MoveToFront(el)
	return b.channel, true
}

// Set binds key to channel for ttl, evicting expired and then least recently
// used entries beyond capacity.
func (s *Store) Set(key [32]byte, channel uuid.UUID, rule string, ttl time.Duration, capacity int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if el, ok := s.m[key]; ok {
		b := el.Value.(*binding)
		b.channel, b.rule, b.ttl, b.expires = channel, rule, ttl, now.Add(ttl)
		s.ll.MoveToFront(el)
	} else {
		s.m[key] = s.ll.PushFront(&binding{key: key, channel: channel, rule: rule, ttl: ttl, expires: now.Add(ttl)})
	}
	// The back holds the least recently used entries, typically the expired ones.
	for el := s.ll.Back(); el != nil && !now.Before(el.Value.(*binding).expires); el = s.ll.Back() {
		s.remove(el)
	}
	for capacity > 0 && s.ll.Len() > capacity {
		s.remove(s.ll.Back())
	}
}

// Delete removes key's binding.
func (s *Store) Delete(key [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.m[key]; ok {
		s.remove(el)
	}
}

func (s *Store) remove(el *list.Element) {
	delete(s.m, el.Value.(*binding).key)
	s.ll.Remove(el)
}

// Stats counts the live bindings in total and per rule name (expired entries
// are purged first).
func (s *Store) Stats() (int, map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	per := map[string]int{}
	for el := s.ll.Front(); el != nil; {
		next := el.Next()
		b := el.Value.(*binding)
		if now.Before(b.expires) {
			per[b.rule]++
		} else {
			s.remove(el)
		}
		el = next
	}
	return s.ll.Len(), per
}

// Clear removes every binding (rule == "") or those created by rule; it
// returns how many were removed.
func (s *Store) Clear(rule string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rule == "" {
		n := s.ll.Len()
		s.ll.Init()
		s.m = map[[32]byte]*list.Element{}
		return n
	}
	n := 0
	for el := s.ll.Front(); el != nil; {
		next := el.Next()
		if el.Value.(*binding).rule == rule {
			s.remove(el)
			n++
		}
		el = next
	}
	return n
}
