package backend

import "sync"

type KeyedConversationLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedConversationLock
}

type keyedConversationLock struct {
	refs int
	mu   sync.Mutex
}

func NewKeyedConversationLocks() *KeyedConversationLocks {
	return &KeyedConversationLocks{locks: map[string]*keyedConversationLock{}}
}

func (l *KeyedConversationLocks) Lock(key string) func() {
	l.mu.Lock()

	entry := l.locks[key]
	if entry == nil {
		entry = new(keyedConversationLock)
		l.locks[key] = entry
	}

	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()
		l.mu.Lock()

		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}
