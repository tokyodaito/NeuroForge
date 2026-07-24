package router

// Tier is the abstract model tier (spec §19.2). The router never binds a tier
// to a concrete model name — the catalog maps tiers to provider-supplied models
// (rule §36.8, §19.2, §14.3).
type Tier int

const (
	// TierTiny — cheapest, for trivial/mechanical work (C0).
	TierTiny Tier = iota
	// TierSmall — small tasks (C1).
	TierSmall
	// TierStandard — default workhorse (C2).
	TierStandard
	// TierHeavy — strong reasoning, escalated tasks (C3).
	TierHeavy
	// TierFrontier — strongest available, hardest tasks (C4).
	TierFrontier
)

// maxTier is the highest defined coding tier.
const maxTier Tier = TierFrontier

// String returns the spec identifier ("TINY", "SMALL", ...).
func (t Tier) String() string {
	switch t {
	case TierTiny:
		return "TINY"
	case TierSmall:
		return "SMALL"
	case TierStandard:
		return "STANDARD"
	case TierHeavy:
		return "HEAVY"
	case TierFrontier:
		return "FRONTIER"
	}
	return "?"
}

// IsValid reports whether t is a defined tier.
func (t Tier) IsValid() bool {
	return t >= TierTiny && t <= TierFrontier
}

// Tiers enumerates every coding tier, cheapest first.
func Tiers() []Tier {
	return []Tier{TierTiny, TierSmall, TierStandard, TierHeavy, TierFrontier}
}

// Complexity is the task complexity band (spec §18.2, §19.3). Higher means more
// demanding. It drives the base tier in the economic cascade.
type Complexity int

const (
	// C0 — deterministic/mechanical parsing.
	C0 Complexity = iota
	// C1 — cheap classifier level.
	C1
	// C2 — standard model at low confidence.
	C2
	// C3 — heavy reasoning.
	C3
	// C4 — only heavy/frontier models should attempt.
	C4
)

// String returns the spec identifier ("C0".."C4").
func (c Complexity) String() string {
	switch c {
	case C0:
		return "C0"
	case C1:
		return "C1"
	case C2:
		return "C2"
	case C3:
		return "C3"
	case C4:
		return "C4"
	}
	return "?"
}

// IsValid reports whether c is a defined complexity band.
func (c Complexity) IsValid() bool { return c >= C0 && c <= C4 }

// Complexities enumerates every band, simplest first.
func Complexities() []Complexity { return []Complexity{C0, C1, C2, C3, C4} }

// BaseTier returns the spec §19.3 base tier for a complexity band:
//
//	C0 → TINY
//	C1 → SMALL
//	C2 → STANDARD
//	C3 → STANDARD / HEAVY  (prefer HEAVY when preferStrong is true)
//	C4 → HEAVY / FRONTIER  (prefer FRONTIER when preferStrong is true)
func BaseTier(c Complexity, preferStrong bool) Tier {
	switch c {
	case C0:
		return TierTiny
	case C1:
		return TierSmall
	case C2:
		return TierStandard
	case C3:
		if preferStrong {
			return TierHeavy
		}
		return TierStandard
	case C4:
		if preferStrong {
			return TierFrontier
		}
		return TierHeavy
	}
	return TierStandard
}
