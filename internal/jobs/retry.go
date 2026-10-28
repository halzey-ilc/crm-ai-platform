package jobs

import "time"

// Backoff is exponential backoff with full jitter: the delay before attempt
// n+1 is uniform in [0, min(Cap, Base*2^(n-1))].
type Backoff struct {
	Base time.Duration
	Cap  time.Duration
	// Rand returns a float in [0,1). Injected so tests are deterministic.
	Rand func() float64
}

// Delay returns the wait after the given (1-based) failed attempt.
func (b Backoff) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	ceiling := b.Base
	for i := 1; i < attempt && ceiling < b.Cap; i++ {
		ceiling *= 2
	}
	if ceiling > b.Cap {
		ceiling = b.Cap
	}
	return time.Duration(b.Rand() * float64(ceiling))
}
