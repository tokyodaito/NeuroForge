package quota

import (
	"sync"
	"testing"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

func ptrFloat(f float64) *float64 { return &f }

func TestSnapshot_FractionRemaining(t *testing.T) {
	cases := []struct {
		name string
		s    Snapshot
		want float64
	}{
		{"nil remaining", Snapshot{}, -1},
		{"zero limit", Snapshot{Remaining: ptrFloat(10), Limit: ptrFloat(0)}, -1},
		{"half", Snapshot{Remaining: ptrFloat(50), Limit: ptrFloat(100)}, 0.5},
		{"clamped high", Snapshot{Remaining: ptrFloat(150), Limit: ptrFloat(100)}, 1},
		{"clamped low", Snapshot{Remaining: ptrFloat(-5), Limit: ptrFloat(100)}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.FractionRemaining(); got != tc.want {
				t.Fatalf("got %f, want %f", got, tc.want)
			}
		})
	}
}

func TestStateIsRoutable(t *testing.T) {
	routable := []State{StateAvailable, StateLow, StateDegraded, StateUnknown}
	notRoutable := []State{StateExhausted, StateRateLimited, StateAuthRequired}
	for _, s := range routable {
		if !s.IsRoutable() {
			t.Errorf("%s should be routable", s)
		}
	}
	for _, s := range notRoutable {
		if s.IsRoutable() {
			t.Errorf("%s should NOT be routable", s)
		}
	}
}

func TestManager_ExhaustedAccountExcluded(t *testing.T) {
	m := New(Config{FailureThreshold: 3, OpenDuration: time.Second, LowRemainingFraction: 0.2,
		Now: func() time.Time { return time.Now().UTC() }})
	id := AccountID{Engine: "fake", Account: "main"}

	if !m.IsAvailable(id) {
		t.Fatal("unseen account should be available")
	}
	m.RecordFailure(id, protocol.FailureProviderQuota)
	if m.IsAvailable(id) {
		t.Fatal("exhausted account must be excluded from new routes")
	}
	if got := m.Snapshot(id).State; got != StateExhausted {
		t.Fatalf("state = %s, want EXHAUSTED", got)
	}
	if m.BreakerState(id) != BreakerOpen {
		t.Errorf("breaker = %s, want OPEN", m.BreakerState(id))
	}
	why := m.WhyUnavailable(id)
	if why == "" || !contains(why, "exhausted") {
		t.Errorf("WhyUnavailable = %q, want exhausted reason", why)
	}

	// Re-applying an available snapshot clears the exhausted state.
	m.Apply(Snapshot{Account: id, Confidence: ConfExact, State: StateAvailable,
		Remaining: ptrFloat(100), Limit: ptrFloat(100)})
	if !m.IsAvailable(id) {
		t.Fatal("healthy snapshot should restore availability")
	}
}

func TestManager_RateLimitIsNotExhaustion(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	now := start
	m := New(Config{FailureThreshold: 3, OpenDuration: time.Second, LowRemainingFraction: 0.2,
		Now: func() time.Time { return now }})

	id := AccountID{Engine: "fake", Account: "rl"}
	// Apply a rate-limited snapshot with a remaining quota figure; the account
	// still has quota — it is only temporarily rate-limited.
	m.Apply(Snapshot{
		Account: id, Confidence: ConfProviderReported, State: StateRateLimited,
		Remaining: ptrFloat(80), Limit: ptrFloat(100), RetryAfter: 2 * time.Second,
		ObservedAt: now,
	})
	if m.IsAvailable(id) {
		t.Fatal("rate-limited account within retry-after window should be excluded")
	}
	if got := m.Snapshot(id).State; got != StateRateLimited {
		t.Fatalf("state = %s, want RATE_LIMITED (distinct from EXHAUSTED)", got)
	}
	why := m.WhyUnavailable(id)
	if !contains(why, "rate-limited") {
		t.Errorf("WhyUnavailable = %q, want rate-limited reason", why)
	}

	// After retry-after elapses, the account is routable again — without losing quota.
	now = now.Add(3 * time.Second)
	if !m.IsAvailable(id) {
		t.Fatal("account should become available after retry-after window")
	}
}

func TestManager_RateLimitDoesNotExhaust(t *testing.T) {
	m := New(DefaultConfig())
	id := AccountID{Engine: "fake", Account: "rl2"}
	// Repeated rate-limit failures must NOT flip the account to EXHAUSTED.
	for i := 0; i < 5; i++ {
		m.RecordFailure(id, protocol.FailureProviderRateLimit)
	}
	if got := m.Snapshot(id).State; got == StateExhausted {
		t.Fatalf("rate-limit must not become exhaustion; state=%s", got)
	}
}

func TestManager_AuthFailureStopsAutoRetry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	m := New(Config{FailureThreshold: 2, OpenDuration: time.Hour, LowRemainingFraction: 0.2,
		Now: func() time.Time { return now }})
	id := AccountID{Engine: "fake", Account: "auth"}

	m.RecordFailure(id, protocol.FailureProviderAuth)
	if m.IsAvailable(id) {
		t.Fatal("auth-failed account must be excluded")
	}
	if got := m.Snapshot(id).State; got != StateAuthRequired {
		t.Fatalf("state = %s, want AUTH_REQUIRED", got)
	}
	why := m.WhyUnavailable(id)
	if !contains(why, "auth") {
		t.Errorf("WhyUnavailable = %q, want auth reason", why)
	}

	// Even after a long time, auth is not auto-cleared.
	now = now.Add(24 * time.Hour)
	if m.IsAvailable(id) {
		t.Fatal("auth failure must not auto-clear; explicit re-auth (healthy Apply) required")
	}

	// A degraded snapshot does NOT clear auth (only a healthy one does).
	m.Apply(Snapshot{Account: id, State: StateDegraded, Confidence: ConfEstimated})
	if m.IsAvailable(id) {
		t.Fatal("degraded snapshot must not clear AUTH_REQUIRED")
	}
	// Healthy snapshot clears it.
	m.Apply(Snapshot{Account: id, State: StateAvailable, Confidence: ConfExact, Remaining: ptrFloat(10), Limit: ptrFloat(10)})
	if !m.IsAvailable(id) {
		t.Fatal("healthy snapshot should clear AUTH_REQUIRED")
	}
}

func TestManager_BreakerTransitions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	m := New(Config{FailureThreshold: 3, OpenDuration: 5 * time.Second, LowRemainingFraction: 0.2,
		Now: func() time.Time { return now }})
	id := AccountID{Engine: "fake", Account: "brk"}

	// Two failures: still closed (below threshold).
	m.RecordFailure(id, protocol.FailureProviderCapacity)
	m.RecordFailure(id, protocol.FailureProviderCapacity)
	if got := m.BreakerState(id); got != BreakerClosed {
		t.Fatalf("breaker = %s, want CLOSED before threshold", got)
	}
	// Third trips it open.
	m.RecordFailure(id, protocol.FailureProviderCapacity)
	if got := m.BreakerState(id); got != BreakerOpen {
		t.Fatalf("breaker = %s, want OPEN after threshold", got)
	}
	if m.IsAvailable(id) {
		t.Fatal("open breaker must exclude account")
	}
	// After OpenDuration -> HALF_OPEN (probe permitted).
	now = now.Add(6 * time.Second)
	if got := m.BreakerState(id); got != BreakerHalfOpen {
		t.Fatalf("breaker = %s, want HALF_OPEN after open duration", got)
	}
	if !m.IsAvailable(id) {
		t.Fatal("half-open breaker should permit one probe")
	}
	// A success closes it.
	m.RecordSuccess(id)
	if got := m.BreakerState(id); got != BreakerClosed {
		t.Fatalf("breaker = %s, want CLOSED after probe success", got)
	}
}

func TestManager_LowWaterMark(t *testing.T) {
	m := New(Config{FailureThreshold: 3, OpenDuration: time.Second, LowRemainingFraction: 0.2,
		Now: func() time.Time { return time.Now().UTC() }})
	id := AccountID{Engine: "fake", Account: "low"}
	m.Apply(Snapshot{Account: id, Confidence: ConfExact, State: StateAvailable,
		Remaining: ptrFloat(10), Limit: ptrFloat(100)}) // 10%
	if got := m.Snapshot(id).State; got != StateLow {
		t.Fatalf("state = %s, want LOW below water mark", got)
	}
	if !m.IsAvailable(id) {
		t.Error("LOW account should still be routable")
	}
}

func TestFormatRemaining_NeverShowsEstimatedAsExact(t *testing.T) {
	cases := []struct {
		name string
		s    Snapshot
		want string
	}{
		{"exact", Snapshot{Confidence: ConfExact, Remaining: ptrFloat(125000)}, "125k"},
		{"provider-reported", Snapshot{Confidence: ConfProviderReported, Remaining: ptrFloat(125000)}, "125k"},
		{"estimated gets tilde", Snapshot{Confidence: ConfEstimated, Remaining: ptrFloat(125000)}, "~125k"},
		{"inferred gets tilde", Snapshot{Confidence: ConfInferred, Remaining: ptrFloat(125000)}, "~125k"},
		{"unknown", Snapshot{Confidence: ConfUnknown, Remaining: nil}, "unknown"},
		{"unknown with value still unknown", Snapshot{Confidence: ConfUnknown, Remaining: ptrFloat(50)}, "unknown"},
		{"million suffix", Snapshot{Confidence: ConfExact, Remaining: ptrFloat(1_400_000)}, "1.4M"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatRemaining(tc.s); got != tc.want {
				t.Fatalf("FormatRemaining = %q, want %q", got, tc.want)
			}
		})
	}
	// Property: estimated/inferred MUST start with "~", exact/reported MUST NOT.
	checks := []Snapshot{
		{Confidence: ConfEstimated, Remaining: ptrFloat(1)},
		{Confidence: ConfInferred, Remaining: ptrFloat(999)},
	}
	for _, s := range checks {
		if r := FormatRemaining(s); r == "unknown" || r[0] != '~' {
			t.Errorf("estimated/inferred must be tilde-prefixed, got %q", r)
		}
	}
}

func TestManager_ConcurrentApplyAndRead(t *testing.T) {
	m := New(DefaultConfig())
	const writers = 8
	const iters = 200
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := AccountID{Engine: "fake", Account: "acc"}
			for j := 0; j < iters; j++ {
				if (j+n)%2 == 0 {
					m.Apply(Snapshot{Account: id, State: StateAvailable, Confidence: ConfExact,
						Remaining: ptrFloat(50), Limit: ptrFloat(100)})
				} else {
					m.RecordFailure(id, protocol.FailureProviderRateLimit)
				}
				_ = m.IsAvailable(id)
				_ = m.Snapshot(id)
				_ = m.All()
			}
		}(i)
	}
	wg.Wait()
}

func TestAllSortedAndComplete(t *testing.T) {
	m := New(DefaultConfig())
	ids := []AccountID{
		{Engine: "b", Account: "z"},
		{Engine: "a", Account: "y"},
		{Engine: "a", Account: "x"},
	}
	for _, id := range ids {
		m.Apply(Snapshot{Account: id, State: StateAvailable, Confidence: ConfExact, Remaining: ptrFloat(1), Limit: ptrFloat(1)})
	}
	all := m.All()
	if len(all) != 3 {
		t.Fatalf("len(All) = %d, want 3", len(all))
	}
	if all[0].Account.Engine != "a" || all[0].Account.Account != "x" {
		t.Errorf("All() not sorted: %+v", all[0])
	}
	if all[2].Account.Engine != "b" {
		t.Errorf("All() not sorted: %+v", all[2])
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
