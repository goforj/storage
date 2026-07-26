package storage

import (
	"fmt"
	"sync/atomic"
)

var testDriverSequence atomic.Uint64

// uniqueTestDriverName prevents process-wide registry collisions across repeated test runs.
func uniqueTestDriverName(base string) string {
	return fmt.Sprintf("%s-%d", base, testDriverSequence.Add(1))
}
