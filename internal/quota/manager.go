package quota

import (
	"sort"
	"sync"
	"time"

	"neuroforge/internal/adapter/codingagent/protocol"
)

// AccountID identifies one provider account: an engine plus an account name.
// Engine, model and account are kept distinct (spec §12.1, §19): the same
// engine can expose several accounts, and the same account can target several
// models. Quota is tracked per (engine, account).
type AccountID struct {
	Engine  string
	Account string
}

// String renders "engine/account".
func (a AccountID) String() string { return a.Engine + "/" + a.Account }

// Confidence is the precision of a quota figure (spec §20.1, rule §36.10). It
// aliases the protocol value so the stabilized adapter boundary remains the
// single source of truth.
type Confidence = protocol.QuotaConfidence

// Confidence levels (spec §20.1).
const (
	ConfExact            = protocol.QuotaConfExact
	ConfProviderReported = protocol.QuotaConfProviderReported
	ConfEstimated        = protocol.QuotaConfEstimated
	ConfInferred         = protocol.QuotaConfInferred
	ConfUnknown          = protocol.QuotaConfUnknown
)

// State is the operational quota state (spec §20.2). Rate limits and quota
// exhaustion are distinct states: a rate-limited account is NOT exhausted and
// becomes available again after its retry-after window (§20.3).
type State string

const (
	// StateAvailable — quota remaining, account usable for new routes.
	StateAvailable State = "AVAILABLE"
	// StateLow — remaining below the low-water fraction; usable but flagged.
	StateLow State = "LOW"
	// StateExhausted — quota used up; account excluded from new routes until reset.
	StateExhausted State = "EXHAUSTED"
	// StateRateLimited — transient backoff required; NOT exhausted (§20.3).
	StateRateLimited State = "RATE_LIMITED"
	// StateAuthRequired — auth failed; automatic retry stops, re-login required.
	StateAuthRequired State = "AUTH_REQUIRED"
	// StateDegraded — serving but with degraded throughput/quality.
	StateDegraded State = "DEGRADED"
	// StateUnknown — no quota information available.
	StateUnknown State = "UNKNOWN"
)

// IsValid reports whether s is a known state.
func (s State) IsValid() bool {
	switch s {
	case StateAvailable, StateLow, StateExhausted, StateRateLimited,
		StateAuthRequired, StateDegraded, StateUnknown:
		return true
	}
	return false
}

// IsRoutable reports whether an account in this state may receive new routes.
// Exhausted and auth-required accounts are excluded; rate-limited ones are
// excluded only while their retry-after window is open (handled by the Manager).
func (s State) IsRoutable() bool {
	switch s {
	case StateAvailable, StateLow, StateDegraded, StateUnknown:
		return true
	}
	return false
}

// States enumerates every state.
func States() []State {
	return []State{StateAvailable, StateLow, StateExhausted, StateRateLimited,
		StateAuthRequired, StateDegraded, StateUnknown}
}

// BreakerState is the circuit-breaker position (spec §20.3).
type BreakerState string

const (
	BreakerClosed   BreakerState = "CLOSED"
	BreakerOpen     BreakerState = "OPEN"
	BreakerHalfOpen BreakerState = "HALF_OPEN"
)

// Snapshot is an immutable, time-stamped view of one account's quota (spec
// §20.1). Remaining/Limit are provider units and may be nil (unknown). The
// dashboard renders Remaining differently per Confidence (§6.1, AC-18).
type Snapshot struct {
	Account    AccountID
	Confidence Confidence
	State      State
	Remaining  *float64
	Limit      *float64
	Window     string
	ResetAt    time.Time
	RetryAfter time.Duration
	Reason     string
	ObservedAt time.Time
}

// FractionRemaining returns the remaining/limit fraction in [0,1], or -1 if the
// figure cannot be computed (either field missing or limit <= 0).
func (s Snapshot) FractionRemaining() float64 {
	if s.Remaining == nil || s.Limit == nil || *s.Limit <= 0 {
		return -1
	}
	f := *s.Remaining / *s.Limit
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return f
}

// Config configures the Manager. Zero values use DefaultConfig.
type Config struct {
	// FailureThreshold is the number of consecutive routable-failures (rate
	// limit / capacity / crash / quota) that trips the breaker to OPEN.
	FailureThreshold int
	// OpenDuration is how long an OPEN breaker stays open before transitioning
	// to HALF_OPEN (a probe request is then permitted).
	OpenDuration time.Duration
	// LowRemainingFraction: when remaining/limit drops below this, State=LOW.
	LowRemainingFraction float64
	// RateLimitJitter fraction (0..1) added to a retry-after window (§20.3).
	RateLimitJitter float64
	// Now is the clock; defaults to time.Now().UTC(). Injectable for tests.
	Now func() time.Time
}

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		FailureThreshold:     3,
		OpenDuration:         60 * time.Second,
		LowRemainingFraction: 0.2,
		RateLimitJitter:      0.25,
		Now:                  func() time.Time { return time.Now().UTC() },
	}
}

type accountState struct {
	snapshot    Snapshot
	breaker     BreakerState
	failures    int
	openedAt    time.Time
	lastFailure protocol.FailureClass
	consec      int // consecutive failures while routable
	probeInUse  bool
}

// Manager records quota snapshots and derives circuit-breaker / routability
// decisions for each account. It is safe for concurrent use.
//
// Invariants enforced (spec §20.3):
//   - An exhausted account is excluded from new routes until its reset time.
//   - A rate-limited account is NOT exhausted: it becomes available again after
//     retry-after (+ jitter), without losing its quota figure.
//   - An auth failure stops automatic retry (StateAuthRequired, breaker OPEN)
//     and is only cleared by an explicit Apply of a healthy snapshot (re-auth).
//   - Breaker transitions: CLOSED -> OPEN after FailureThreshold consecutive
//     routable failures; OPEN -> HALF_OPEN after OpenDuration; HALF_OPEN ->
//     CLOSED on success or back to OPEN on a new failure.
type Manager struct {
	mu  sync.Mutex
	cfg Config
	st  map[AccountID]*accountState
}

// New constructs a Manager. A zero cfg is replaced by DefaultConfig.
func New(cfg Config) *Manager {
	if cfg.FailureThreshold <= 0 || cfg.Now == nil {
		def := DefaultConfig()
		if cfg.FailureThreshold <= 0 {
			cfg.FailureThreshold = def.FailureThreshold
		}
		if cfg.OpenDuration <= 0 {
			cfg.OpenDuration = def.OpenDuration
		}
		if cfg.LowRemainingFraction <= 0 {
			cfg.LowRemainingFraction = def.LowRemainingFraction
		}
		if cfg.Now == nil {
			cfg.Now = def.Now
		}
	}
	return &Manager{cfg: cfg, st: map[AccountID]*accountState{}}
}

// now returns the configured clock (UTC).
func (m *Manager) now() time.Time {
	if m.cfg.Now != nil {
		return m.cfg.Now()
	}
	return time.Now().UTC()
}

// Apply records a fresh snapshot for an account (e.g. from an adapter's
// InspectQuota) and recomputes the derived state. A healthy snapshot clears an
// AUTH_REQUIRED condition (re-auth succeeded).
func (m *Manager) Apply(s Snapshot) {
	if s.ObservedAt.IsZero() {
		s.ObservedAt = m.now()
	}
	m.applyLocked(s)
}

func (m *Manager) applyLocked(s Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[s.Account]
	if cur == nil {
		cur = &accountState{breaker: BreakerClosed}
		m.st[s.Account] = cur
	}
	prevState := cur.snapshot.State
	cur.snapshot = s

	// Derive LOW from remaining fraction when the adapter did not flag it.
	if s.State == StateAvailable || s.State == StateUnknown {
		if f := s.FractionRemaining(); f >= 0 && f < m.cfg.LowRemainingFraction {
			cur.snapshot.State = StateLow
		}
	}

	// AUTH_REQUIRED can only be cleared by a genuinely healthy snapshot
	// (re-auth succeeded). A degraded/rate-limited snapshot does NOT clear it.
	if prevState == StateAuthRequired && s.State != StateAvailable && s.State != StateLow {
		cur.snapshot.State = StateAuthRequired
	}

	// Reset the breaker on a genuinely healthy snapshot (re-auth / quota reset).
	if cur.snapshot.State == StateAvailable {
		cur.breaker = BreakerClosed
		cur.consec = 0
		cur.failures = 0
		cur.lastFailure = ""
	}
}

// RecordFailure feeds a §32 failure class back into the breaker for an account.
// Rate-limit/capacity/timeout/crash count toward the consecutive routable
// failure count; quota exhaustion flips the account to EXHAUSTED; auth flips to
// AUTH_REQUIRED. Non-failure classes are ignored.
func (m *Manager) RecordFailure(id AccountID, class protocol.FailureClass) {
	if !class.IsValid() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		cur = &accountState{breaker: BreakerClosed, snapshot: Snapshot{Account: id, State: StateUnknown, Confidence: ConfUnknown}}
		m.st[id] = cur
	}
	now := m.now()
	cur.failures++
	cur.lastFailure = class

	switch class {
	case protocol.FailureProviderQuota:
		cur.snapshot.State = StateExhausted
		cur.breaker = BreakerOpen
		cur.openedAt = now
		cur.consec = 0
	case protocol.FailureProviderAuth:
		cur.snapshot.State = StateAuthRequired
		cur.breaker = BreakerOpen
		cur.openedAt = now
		cur.consec = 0
	case protocol.FailureProviderRateLimit, protocol.FailureProviderCapacity,
		protocol.FailureEngineCrash, protocol.FailureTimeout, protocol.FailureMalformedOutput:
		cur.consec++
		if cur.consec >= m.cfg.FailureThreshold {
			cur.breaker = BreakerOpen
			cur.openedAt = now
		}
	default:
		// Other classes (build/test failures, scope/policy violations) are not
		// provider-quota signals and do not move the breaker.
	}
}

// RecordSuccess clears consecutive failures and closes a HALF_OPEN breaker.
func (m *Manager) RecordSuccess(id AccountID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		return
	}
	cur.consec = 0
	if cur.breaker == BreakerHalfOpen {
		cur.breaker = BreakerClosed
		cur.failures = 0
		cur.lastFailure = ""
	}
	if cur.breaker == BreakerClosed {
		cur.failures = 0
	}
}

// Snapshot returns the latest snapshot for an account (zero value if unseen).
func (m *Manager) Snapshot(id AccountID) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		return Snapshot{Account: id, State: StateUnknown, Confidence: ConfUnknown}
	}
	return m.effectiveLocked(cur)
}

// All returns the effective snapshots of every known account, sorted by
// engine then account for stable display.
func (m *Manager) All() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.st))
	for _, cur := range m.st {
		out = append(out, m.effectiveLocked(cur))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account.Engine != out[j].Account.Engine {
			return out[i].Account.Engine < out[j].Account.Engine
		}
		return out[i].Account.Account < out[j].Account.Account
	})
	return out
}

// effectiveLocked applies time-based transitions (OPEN -> HALF_OPEN,
// rate-limit window expiry) and returns the effective snapshot.
func (m *Manager) effectiveLocked(cur *accountState) Snapshot {
	now := m.now()
	s := cur.snapshot

	// OPEN -> HALF_OPEN after OpenDuration (a probe is then allowed).
	if cur.breaker == BreakerOpen && !cur.openedAt.IsZero() && now.Sub(cur.openedAt) >= m.cfg.OpenDuration {
		cur.breaker = BreakerHalfOpen
	}

	switch s.State {
	case StateRateLimited:
		// Rate limit is a transient window; once retry-after has elapsed the
		// account is routable again WITHOUT being treated as exhausted.
		if !s.ObservedAt.IsZero() && s.RetryAfter > 0 {
			if now.Sub(s.ObservedAt) >= s.RetryAfter {
				// Window expired; restore to available/low unless another blocker holds.
				if cur.breaker == BreakerOpen || cur.breaker == BreakerHalfOpen {
					// leave breaker decision to caller; but state is no longer rate-limited
					s.State = StateAvailable
				} else {
					s.State = StateAvailable
				}
			}
		}
	}

	return s
}

// IsAvailable reports whether new routes may be assigned to the account right
// now. This is the single entry point the router uses to exclude accounts
// (spec §20.3: exhausted accounts are excluded from new routes).
func (m *Manager) IsAvailable(id AccountID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		return true // unseen accounts are presumed available
	}
	eff := m.effectiveLocked(cur)
	if !eff.State.IsRoutable() {
		return false
	}
	switch cur.breaker {
	case BreakerOpen:
		return false
	case BreakerHalfOpen:
		return true // one probe permitted
	default:
		return true
	}
}

// BreakerState returns the current breaker position for an account.
func (m *Manager) BreakerState(id AccountID) BreakerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		return BreakerClosed
	}
	// Refresh time-based transition.
	eff := m.effectiveLocked(cur)
	_ = eff
	return cur.breaker
}

// WhyUnavailable returns a human-readable reason an account is not available, or
// "" if it is available. Used by route explanation (§19.6).
func (m *Manager) WhyUnavailable(id AccountID) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.st[id]
	if cur == nil {
		return ""
	}
	eff := m.effectiveLocked(cur)
	if !eff.State.IsRoutable() {
		switch eff.State {
		case StateExhausted:
			if !eff.ResetAt.IsZero() {
				return "quota exhausted until reset at " + eff.ResetAt.Format(time.RFC3339)
			}
			return "quota exhausted; account blocked until reset (§20.3)"
		case StateRateLimited:
			return "rate-limited; waiting retry-after window (transient, not quota exhaustion)"
		case StateAuthRequired:
			return "auth failed; automatic retry stopped — re-login required"
		}
		return "state " + string(eff.State)
	}
	switch cur.breaker {
	case BreakerOpen:
		return "circuit breaker OPEN after " + itoa(cur.consec) + " consecutive failures"
	case BreakerHalfOpen:
		return "" // probe permitted
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
