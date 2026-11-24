package kernel

import (
	"reflect"

	"gvisor.dev/gvisor/pkg/sync"
	"gvisor.dev/gvisor/pkg/sync/locking"
)

// Mutex is sync.Mutex with the correctness validator.
type cgroupMutex struct {
	mu sync.Mutex
}

var cgroupprefixIndex *locking.MutexClass

// lockNames is a list of user-friendly lock names.
// Populated in init.
var cgrouplockNames []string

// lockNameIndex is used as an index passed to NestedLock and NestedUnlock,
// referring to an index within lockNames.
// Values are specified using the "consts" field of go_template_instance.
type cgrouplockNameIndex int

// DO NOT REMOVE: The following function automatically replaced with lock index constants.
// LOCK_NAME_INDEX_CONSTANTS
const ()

// Lock locks m.
// +checklocksignore
func (m *cgroupMutex) Lock() {
	locking.AddGLock(cgroupprefixIndex, -1) // escapes: lockdep.
	m.mu.Lock()
}

// NestedLock locks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *cgroupMutex) NestedLock(i cgrouplockNameIndex) {
	locking.AddGLock(cgroupprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Lock()
}

// Unlock unlocks m.
// +checklocksignore
func (m *cgroupMutex) Unlock() {
	locking.DelGLock(cgroupprefixIndex, -1) // escapes: lockdep.
	m.mu.Unlock()
}

// NestedUnlock unlocks m knowing that another lock of the same type is held.
// +checklocksignore
func (m *cgroupMutex) NestedUnlock(i cgrouplockNameIndex) {
	locking.DelGLock(cgroupprefixIndex, int(i)) // escapes: lockdep.
	m.mu.Unlock()
}

// LockBypass locks m without executing the validator.
// +checklocksignore
func (m *cgroupMutex) LockBypass() {
	m.mu.Lock()
}

// UnlockBypass undoes a single LockBypass call.
// +checklocksignore
func (m *cgroupMutex) UnlockBypass() {
	m.mu.Unlock()
}

// DO NOT REMOVE: The following function is automatically replaced.
func cgroupinitLockNames() {}

func init() {
	cgroupinitLockNames()
	cgroupprefixIndex = locking.NewMutexClass(reflect.TypeFor[cgroupMutex](), cgrouplockNames)
}
