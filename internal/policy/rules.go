package policy

import "fmt"

// Adjustment records a dependency-rule change applied by [Normalize]. It makes
// normalisation observable and auditable (no silent mutation).
type Adjustment struct {
	Field  string // dotted path, e.g. "merge.enabled"
	From   string
	To     string
	Reason string
}

// Normalize applies the §5.1 dependency rules to a copy of p and returns the
// normalised pipeline plus the adjustments it made. Normalisation never weakens
// security: it only forces downstream toggles OFF when an upstream capability is
// off.
//
// Rules (spec §5.1, §24.2):
//
//	R1  git.push=false             ⇒ change_request.create=false
//	R2  git.push=false             ⇒ merge=false
//	R3  git.push=false             ⇒ post_merge.enabled=false
//	R4  merge=false                ⇒ post_merge.enabled=false
//	R5  change_request.create=false & merge=true ⇒ local-merge mode (info only)
//	R6  tests.generate=false       ⇒ tests.modify_existing=false (§24.2: a disabled
//	    generation stage forbids creating OR modifying test files)
//	R7  tests.generate=false       ⇒ tests.run_generated=false (nothing to run)
//
// Normalize is idempotent: normalising an already-normalised pipeline is a no-op.
func Normalize(p Pipeline) (Pipeline, []Adjustment) {
	out := p
	var adj []Adjustment

	// R1–R3: push=false cascades to change_request/merge/post_merge.
	if !p.Git.Push {
		if p.ChangeRequest.Create {
			adj = append(adj, Adjustment{
				Field: "change_request.create", From: "true", To: "false",
				Reason: "git.push=false (§5.1 R1)",
			})
			out.ChangeRequest.Create = false
		}
		if p.Merge {
			adj = append(adj, Adjustment{
				Field: "merge.enabled", From: "true", To: "false",
				Reason: "git.push=false (§5.1 R2)",
			})
			out.Merge = false
		}
		if p.PostMerge.Enabled {
			adj = append(adj, Adjustment{
				Field: "post_merge.enabled", From: "true", To: "false",
				Reason: "git.push=false (§5.1 R3)",
			})
			out.PostMerge.Enabled = false
		}
	}

	// R4: merge=false forces post_merge off (independent of push).
	if !out.Merge && out.PostMerge.Enabled {
		adj = append(adj, Adjustment{
			Field: "post_merge.enabled", From: "true", To: "false",
			Reason: "merge=false (§5.1 R4)",
		})
		out.PostMerge.Enabled = false
	}

	// R5: change_request.create=false & merge=true is the "local merge" mode —
	// a permitted configuration. Record it for transparency.
	if !out.ChangeRequest.Create && out.Merge {
		adj = append(adj, Adjustment{
			Field: "merge.mode", From: "remote", To: "local-only",
			Reason: "change_request.create=false (§5.1 R5: local merge mode)",
		})
	}

	// R6: tests.generate=false forces modify_existing off (§24.2: a disabled
	// generation stage forbids creating OR modifying test files; the scope
	// validator enforces this at the path level, and the toggle is normalised
	// here so callers see the effective value).
	if !out.Tests.Generate && out.Tests.ModifyExisting {
		adj = append(adj, Adjustment{
			Field: "tests.modify_existing", From: "true", To: "false",
			Reason: "tests.generate=false (§24.2 R6: test paths forbidden)",
		})
		out.Tests.ModifyExisting = false
	}

	// R7: tests.generate=false forces run_generated off (nothing generated to run).
	if !out.Tests.Generate && out.Tests.RunGenerated {
		adj = append(adj, Adjustment{
			Field: "tests.run_generated", From: "true", To: "false",
			Reason: "tests.generate=false (§24.2 R7: no generated tests to run)",
		})
		out.Tests.RunGenerated = false
	}

	return out, adj
}

// Severity of a [Violation].
type Severity int

const (
	SeverityInfo  Severity = iota // informational, policy still resolved
	SeverityWarn                  // suspicious but resolvable
	SeverityBlock                 // hard violation; the offending action must be denied
)

// String returns a stable label.
func (s Severity) String() string {
	switch s {
	case SeverityBlock:
		return "block"
	case SeverityWarn:
		return "warn"
	default:
		return "info"
	}
}

// Violation is a structured policy problem surfaced by [Resolve] or [Validate].
type Violation struct {
	Rule     string // e.g. "ac29.override-clamp", "§5.1.vv-required-for-ui-merge"
	Severity Severity
	Detail   string
}

// Error makes Violation usable as an error for hard blocks.
func (v Violation) Error() string {
	return fmt.Sprintf("policy violation [%s] %s: %s", v.Severity, v.Rule, v.Detail)
}

// IsBlock reports whether v is a hard violation.
func (v Violation) IsBlock() bool { return v.Severity == SeverityBlock }

// Blocks reports whether any violation in vs is a hard block.
func Blocks(vs []Violation) bool {
	for _, v := range vs {
		if v.IsBlock() {
			return true
		}
	}
	return false
}
