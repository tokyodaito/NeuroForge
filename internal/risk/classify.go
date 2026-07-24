package risk

import "strings"

// Level is the risk band (spec §26). Higher means more sensitive.
type Level int

const (
	// R0 — documentation and mechanical changes.
	R0 Level = iota
	// R1 — local UI, analytics, simple logic.
	R1
	// R2 — public API, provider integration, background jobs.
	R2
	// R3 — migrations, concurrency, subscriptions.
	R3
	// R4 — auth, payments, permissions, destructive changes.
	R4
)

// MaxLevel is the highest defined risk band.
const MaxLevel Level = R4

// String returns the spec identifier (e.g. "R3").
func (l Level) String() string {
	switch l {
	case R0:
		return "R0"
	case R1:
		return "R1"
	case R2:
		return "R2"
	case R3:
		return "R3"
	case R4:
		return "R4"
	}
	return "R?"
}

// IsValid reports whether l is a defined band.
func (l Level) IsValid() bool {
	return l >= R0 && l <= R4
}

// AtLeast reports whether l >= threshold.
func (l Level) AtLeast(threshold Level) bool { return l >= threshold }

// Levels enumerates every band, lowest first.
func Levels() []Level { return []Level{R0, R1, R2, R3, R4} }

// Signals is the structured input produced by deterministic change analysis
// (task spec, touched paths, planned commands). The classifier never inspects
// raw natural-language intent through an LLM — callers extract the boolean and
// lexical signals first (rule §22.6).
type Signals struct {
	// Free-form description and touched file paths; scanned for keywords only.
	Description string
	Paths       []string

	// Structural flags. A set flag always dominates lower-band keyword hints
	// for the same dimension, so a path under auth/ with no keywords still
	// classifies at R4.
	TouchesAuth         bool
	TouchesPayments     bool
	TouchesPermissions  bool
	DestructiveCommands bool // rm -rf, force-push to shared, drop table, etc.
	HasMigrations       bool // DB migrations / schema-altering scripts
	ConcurrencyChange   bool // locks, transactions, goroutine fan-out touched
	SubscriptionChange  bool // billing/subscription contracts touched
	PublicAPIChange     bool // exported API / versioned contracts changed
	TouchesInfra        bool // CI/IaC/runtime config changed
}

// Result is the classification outcome. Reasons are stable, human-readable and
// ordered by descending influence so a route explanation can show why the band
// was chosen (§19.6).
type Result struct {
	Level   Level
	Reasons []string
}

// Classify maps signals onto the §26 taxonomy deterministically. The highest
// band implied by any signal wins; reasons list every contributing signal.
//
// Precedence (highest to lowest), mirroring §26:
//
//	R4 — auth/payments/permissions/destructive
//	R3 — migrations/concurrency/subscriptions
//	R2 — public API / provider integration / background jobs
//	R1 — local UI / analytics / simple logic
//	R0 — docs and mechanical changes (default floor)
func Classify(s Signals) Result {
	var reasons []string
	level := R0

	// ---- R4: security-/money-/destructive-class signals (hard floor R4) ----
	if s.TouchesAuth {
		level = maxLevel(level, R4)
		reasons = append(reasons, "touches authentication")
	}
	if s.TouchesPayments {
		level = maxLevel(level, R4)
		reasons = append(reasons, "touches payments/billing")
	}
	if s.TouchesPermissions {
		level = maxLevel(level, R4)
		reasons = append(reasons, "touches permissions/authorization")
	}
	if s.DestructiveCommands {
		level = maxLevel(level, R4)
		reasons = append(reasons, "uses destructive commands")
	}

	// ---- R3: data-integrity / concurrency / subscription contracts ----
	if s.HasMigrations {
		level = maxLevel(level, R3)
		reasons = append(reasons, "includes database migrations")
	}
	if s.ConcurrencyChange {
		level = maxLevel(level, R3)
		reasons = append(reasons, "changes concurrency/locking")
	}
	if s.SubscriptionChange {
		level = maxLevel(level, R3)
		reasons = append(reasons, "changes subscription contracts")
	}

	// ---- R2: integration surface ----
	if s.PublicAPIChange {
		level = maxLevel(level, R2)
		reasons = append(reasons, "changes public API/contracts")
	}
	if s.TouchesInfra {
		level = maxLevel(level, R2)
		reasons = append(reasons, "changes CI/infrastructure/runtime config")
	}

	// ---- keyword hints (lower confidence; never raise above a structural floor) ----
	if kw, band := keywordHint(s.Description); kw != "" {
		level = maxLevel(level, band)
		reasons = append(reasons, "keyword hint: "+kw)
	}
	if pk, band := pathHint(s.Paths); pk != "" {
		level = maxLevel(level, band)
		reasons = append(reasons, "path hint: "+pk)
	}

	if len(reasons) == 0 {
		reasons = []string{"documentation/mechanical change (no elevated-risk signal)"}
	}
	return Result{Level: level, Reasons: reasons}
}

func maxLevel(a, b Level) Level {
	if a > b {
		return a
	}
	return b
}

// keywordHint scans the description for risk-relevant terms. The classifier is
// deliberately conservative: terms map to the *floor* band a provider assigns
// such signals in §26, and a structural flag always wins.
func keywordHint(desc string) (string, Level) {
	low := strings.ToLower(desc)
	if low == "" {
		return "", R0
	}
	// R4 terms
	for _, term := range []string{"password", "secret", "api key", "api_key", "oauth", "session token", "2fa", "mfa", "refund", "charge", "stripe", "payment", "delete user", "drop table", "force push"} {
		if strings.Contains(low, term) {
			return term, R4
		}
	}
	// R3 terms
	for _, term := range []string{"migration", "migrate", "transaction", "lock", "mutex", "subscribe", "subscription", "billing plan", "cron", "scheduler"} {
		if strings.Contains(low, term) {
			return term, R3
		}
	}
	// R2 terms
	for _, term := range []string{"public api", "webhook", "integration", "background job", "queue", "provider"} {
		if strings.Contains(low, term) {
			return term, R2
		}
	}
	// R1 terms
	for _, term := range []string{"analytics", "dashboard", "ui ", "button", "component", "chart", "report"} {
		if strings.Contains(low, term) {
			return term, R1
		}
	}
	return "", R0
}

// pathHint scans touched paths for risk-relevant directory/file patterns.
func pathHint(paths []string) (string, Level) {
	for _, p := range paths {
		low := strings.ToLower(p)
		switch {
		case containsAny(low, "auth", "/auth", "permission", "acl", "payment", "billing", "secret", "credential", "token"):
			return p, R4
		case containsAny(low, "migration", "migrations", "schema", "lock", "subscription"):
			return p, R3
		case containsAny(low, "api/", "handler", "webhook", "integration", "queue", "worker"):
			return p, R2
		case containsAny(low, "ui/", "component", "screen", "analytics", "dashboard"):
			return p, R1
		}
	}
	return "", R0
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
