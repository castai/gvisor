package fsutil

import (
	"reflect"

	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/sync/locking"
)

// Mutex is sync.Mutex with the correctness validator.
type refsMutex struct {
	mu sync.Mutex
}

var refsprefixIndex *locking.MutexClass

// lockNames is a list of user-friendly lock names.
// Populated in init.
var refslockNames []string

// lockNameIndex is used as an index passed to NestedLock and NestedUnlock,
// referring to an index within lockNames.
// Values are specified using the "consts" field of go_template_instance.
type refslockNameIndex int

// DO NOT REMOVE: The following function automatically replaced with lock index constants.
// LOCK_NAME_INDEX_CONSTANTS
const ()

// Lock locks m.
// +checklocksignore
func (m *refsMutex) Lock() {
	locking.AddGLock(refsprefixIndex, -1) // escapes: lockdep.
	m.mu.Lock()
}

// NestedLock locks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *refsMutex) NestedLock(i refslockNameIndex) {
	locking.AddGLock(refsprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Lock()
}

// Unlock unlocks m.
// +checklocksignore
func (m *refsMutex) Unlock() {
	locking.DelGLock(refsprefixIndex, -1) // escapes: lockdep.
	m.mu.Unlock()
}

// NestedUnlock unlocks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *refsMutex) NestedUnlock(i refslockNameIndex) {
	locking.DelGLock(refsprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Unlock()
}

// LockBypass locks m without executing the validator.
// +checklocksignore
func (m *refsMutex) LockBypass() {
	m.mu.Lock()
}

// UnlockBypass undoes a single LockBypass call.
// +checklocksignore
func (m *refsMutex) UnlockBypass() {
	m.mu.Unlock()
}

// DO NOT REMOVE: The following function is automatically replaced.
func refsinitLockNames() {}

func init() {
	refsinitLockNames()
	refsprefixIndex = locking.NewMutexClass(reflect.TypeFor[refsMutex](), refslockNames)
}
