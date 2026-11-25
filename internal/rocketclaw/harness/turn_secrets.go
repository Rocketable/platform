package harness

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"runtime/secret"
	"slices"
	"strings"
	"sync"
)

var turnSecretNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_(TOKEN|KEY|SECRET|PASSWORD)$`)

type TurnSecretError struct {
	Name, Problem string
}

func (e *TurnSecretError) Error() string {
	return fmt.Sprintf("secret %q: %s", e.Name, e.Problem)
}

type TurnSecretStore struct {
	mu     sync.Mutex
	values map[string][][]byte
}

func NewTurnSecretStore() *TurnSecretStore {
	return &TurnSecretStore{values: map[string][][]byte{}}
}

func (s *TurnSecretStore) Save(name string, value []byte) error {
	switch {
	case len(name) > 64 || !turnSecretNamePattern.MatchString(name):
		return &TurnSecretError{Name: name, Problem: "the name must be at most 64 bytes of A-Z, 0-9 and _, start with a letter, and end in _TOKEN, _KEY, _SECRET or _PASSWORD"}
	case len(value) < 8 || len(value) > 64<<10 || bytes.ContainsAny(value, "\x00\r\n"):
		return &TurnSecretError{Name: name, Problem: "the value must be a single line of 8 bytes to 64 KiB with no NUL byte"}
	}

	held := make([]byte, len(value))

	secret.Do(func() {
		copy(held, value)
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	s.values[name] = append(s.values[name], held)

	return nil
}

func (s *TurnSecretStore) Holds(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.values[name]

	return ok
}

func (s *TurnSecretStore) Env() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	env := make([]string, 0, len(s.values))

	secret.Do(func() {
		for _, name := range slices.Sorted(maps.Keys(s.values)) {
			env = append(env, name+"="+string(s.values[name][len(s.values[name])-1]))
		}
	})

	return env
}

func (s *TurnSecretStore) Redact(text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	type held struct {
		name  string
		value []byte
	}

	var all []held

	for name, values := range s.values {
		for _, value := range values {
			all = append(all, held{name, value})
		}
	}

	slices.SortFunc(all, func(a, b held) int {
		return cmp.Or(cmp.Compare(len(b.value), len(a.value)), strings.Compare(a.name, b.name))
	})

	secret.Do(func() {
		for _, h := range all {
			text = strings.ReplaceAll(text, string(h.value), "[secret:"+h.name+"]")
		}
	})

	return text
}

func (s *TurnSecretStore) Erase() {
	s.mu.Lock()
	defer s.mu.Unlock()

	secret.Do(func() {
		for _, values := range s.values {
			for _, value := range values {
				clear(value)
			}
		}
	})

	clear(s.values)
}
