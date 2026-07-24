package supervisor

import (
	"math/rand"
	"sync"
	"time"
)

// jitterPool is the source for deterministic-enough jitter in cooldowns. It is
// seeded once and guarded by a mutex (math/rand sources are not safe for
// concurrent use). The supervisor deliberately does NOT seed from time for
// unit tests; production wiring may override [RecoveryClassifier.Rand].
var (
	jitterMu   sync.Mutex
	jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// defaultRand returns a float64 in [0,1) for cooldown jitter (§20.3).
func defaultRand() float64 {
	jitterMu.Lock()
	defer jitterMu.Unlock()
	return jitterRand.Float64()
}
