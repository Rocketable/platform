package tcp

import (
	"reflect"

	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/sync/locking"
)

// Mutex is sync.Mutex with the correctness validator.
type acceptMutex struct {
	mu sync.Mutex
}

var acceptprefixIndex *locking.MutexClass

// lockNames is a list of user-friendly lock names.
// Populated in init.
var acceptlockNames []string

// lockNameIndex is used as an index passed to NestedLock and NestedUnlock,
// referring to an index within lockNames.
// Values are specified using the "consts" field of go_template_instance.
type acceptlockNameIndex int

// DO NOT REMOVE: The following function automatically replaced with lock index constants.
// LOCK_NAME_INDEX_CONSTANTS
const ()

// Lock locks m.
// +checklocksignore
func (m *acceptMutex) Lock() {
	locking.AddGLock(acceptprefixIndex, -1) // escapes: lockdep.
	m.mu.Lock()
}

// NestedLock locks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *acceptMutex) NestedLock(i acceptlockNameIndex) {
	locking.AddGLock(acceptprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Lock()
}

// Unlock unlocks m.
// +checklocksignore
func (m *acceptMutex) Unlock() {
	locking.DelGLock(acceptprefixIndex, -1) // escapes: lockdep.
	m.mu.Unlock()
}

// NestedUnlock unlocks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *acceptMutex) NestedUnlock(i acceptlockNameIndex) {
	locking.DelGLock(acceptprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Unlock()
}

// LockBypass locks m without executing the validator.
// +checklocksignore
func (m *acceptMutex) LockBypass() {
	m.mu.Lock()
}

// UnlockBypass undoes a single LockBypass call.
// +checklocksignore
func (m *acceptMutex) UnlockBypass() {
	m.mu.Unlock()
}

// DO NOT REMOVE: The following function is automatically replaced.
func acceptinitLockNames() {}

func init() {
	acceptinitLockNames()
	acceptprefixIndex = locking.NewMutexClass(reflect.TypeFor[acceptMutex](), acceptlockNames)
}
