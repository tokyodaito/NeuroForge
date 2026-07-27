package task

import (
	"fmt"
	"strings"

	"neuroforge/internal/risk"
)

// This file implements the deterministic Task Compiler (spec §18.1, §18.2, §9).
//
// The compiler transforms free-form task text + attachment metadata into a
// structured Specification (the durable model added by M14-01). The compiler is
// deliberately PURE: it performs no I/O (no daemon, no storage, no clock, no
// external model call) so identical input deterministically produces identical
// output. Persistence and versioning are the caller's responsibility — the
// returned Specification always has Version=0 and Locked=false.
//
// Layering: this file lives in package task alongside the Specification model
// it produces (per AGENTS.md, internal/task owns "Tasks / compiler"). It reuses
// the deterministic risk classifier from internal/risk (already accepted at
// M6-3) rather than duplicating the §26 taxonomy.

// CompileInput is the free-form input to the deterministic compiler.
type CompileInput struct {
	TaskID      string
	Title       string
	Description string
	Priority    Priority
	Attachments []Attachment
}

// Confidence expresses how deterministic the compiled specification is. It is
// advisory: even a LOW-confidence result is a valid Specification that the
// caller may persist (validation still passes); the value tells the caller
// whether to ask a human before locking the spec.
type Confidence string

const (
	// ConfidenceHigh — the input had structured sections (explicit objective and
	// acceptance criteria). The compiler did not have to synthesise anything.
	ConfidenceHigh Confidence = "HIGH"
	// ConfidenceMedium — the compiler made safe, reversible assumptions
	// (e.g. synthesised a default AC from a clear objective). Captured in
	// UncertaintyReasons.
	ConfidenceMedium Confidence = "MEDIUM"
	// ConfidenceLow — the input was too vague to be confidently actionable
	// (e.g. empty description, attachment-only, or a one-word "fix it"). At
	// least one Clarification is present.
	ConfidenceLow Confidence = "LOW"
)

// IsValid reports whether c is a known confidence value.
func (c Confidence) IsValid() bool {
	switch c {
	case "", ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return true
	}
	return false
}

// Clarification is one explicit open question that the compiler could not
// resolve with a safe assumption (spec §9.7: a question is asked only if a safe
// reversible assumption is impossible, options lead to materially different
// product, a policy action is required, or the target cannot be determined).
type Clarification struct {
	// Question is the short, human-readable question.
	Question string
	// Reason explains why the compiler could not pick a safe assumption.
	Reason string
	// Options lists the disambiguations the compiler can identify, if any. The
	// options are advisory: an empty list means the compiler cannot enumerate
	// them deterministically.
	Options []string
}

// CompileResult bundles the compiled specification + diagnostics.
type CompileResult struct {
	Specification      Specification
	Confidence         Confidence
	UncertaintyReasons []string
	Clarifications     []Clarification
	RiskReasons        []string
	ComplexityReasons  []string
	// AttachmentRoles maps attachment hash → role. Mirrors §18.1's
	// "attachment roles" output: the compiler does not invent roles, it surfaces
	// the metadata it was given so the caller can persist them.
	AttachmentRoles map[string]AttachmentRole
}

// Compile transforms free-form input into a structured specification (spec
// §18.1, §18.2 economic cascade: deterministic parsing → cheap classifier).
//
// The compiler is pure: identical input produces identical output. It does not
// perform any I/O. If the input is too vague to produce a valid Specification
// (no TaskID, or no description AND no attachment), the compiler returns the
// (possibly invalid) result plus diagnostics; callers must run
// ValidateSpecification before persisting. A return error is reserved for hard
// contract violations (currently: missing TaskID — surfaced as
// ErrInvalidSpecification so the caller can branch with errors.Is).
func Compile(in CompileInput) (CompileResult, error) {
	res := CompileResult{
		AttachmentRoles: collectAttachmentRoles(in.Attachments),
	}

	// Parse structured sections from the description.
	sections := parseSections(in.Description)

	// Derive the objective: explicit section > title > synthesised from text.
	objective, objSource := deriveObjective(in, sections)
	res.Specification.TaskID = in.TaskID
	res.Specification.Objective = objective

	// Acceptance criteria: explicit section > synthesised from objective.
	acs, acsSynthesised := deriveAcceptanceCriteria(sections, objective)
	res.Specification.AcceptanceCriteria = acs
	if acsSynthesised {
		res.UncertaintyReasons = append(res.UncertaintyReasons,
			"acceptance criteria not stated explicitly; synthesised default AC from objective")
	}

	// Optional structured sections (may be empty).
	res.Specification.NonGoals = sections.list("non-goals")
	res.Specification.Assumptions = sections.list("assumptions")
	res.Specification.Constraints = sections.list("constraints")
	res.Specification.ProposedScope = sections.list("scope")

	// Risk classifier cascade: deterministic risk.Classify over description +
	// attachment filenames (paths). This is the second stage of the §18.2
	// cascade — no external model call.
	riskRes := classifyRisk(in)
	res.Specification.Risk = toTaskRisk(riskRes.Level)
	res.RiskReasons = riskRes.Reasons

	// Complexity classifier cascade: deterministic local classifier.
	cx := classifyComplexity(in, sections)
	res.Specification.Complexity = cx.Complexity
	res.ComplexityReasons = cx.Reasons

	// Visual requirements: required iff a DESIGN_REFERENCE or BUG_SCREENSHOT
	// attachment is present. References list the attachment hashes (§15).
	res.Specification.VisualRequirements = deriveVisualRequirements(in.Attachments)

	// Confidence + clarifications.
	res.Confidence, res.Clarifications = deriveConfidenceAndClarifications(
		in, objSource, acsSynthesised, res.Specification, riskRes)

	// Hard contract: missing TaskID is a return error so callers can branch
	// with errors.Is(err, ErrInvalidSpecification). Other validation problems
	// (empty objective, no ACs) are surfaced via Confidence=LOW and the
	// compiled spec is left to fail ValidateSpecification.
	if in.TaskID == "" {
		return res, fmt.Errorf("%w: task_id is required", ErrInvalidSpecification)
	}

	return res, nil
}

// parseSections extracts structured sections from the free-form description. A
// section header is a line that starts with one of the known labels followed by
// ":" (case-insensitive, leading whitespace allowed, trailing whitespace
// ignored). The body extends until the next header or end-of-input.
//
// Items inside a list-shaped section (non-goals, assumptions, constraints,
// scope, acceptance criteria) are split on bullet/numbered list markers. The
// objective section preserves its raw text (trimmed, single-spaced).
//
// Unknown sections are ignored (kept in the description, not lost). Lines
// before the first recognised header become the implicit "preamble" — used by
// deriveObjective when no explicit "Objective:" section exists.
func parseSections(description string) parsedSections {
	secs := parsedSections{
		headers: map[string]bool{},
		items:   map[string][]string{},
	}
	if description == "" {
		return secs
	}

	lines := normaliseLines(description)
	var preamble []string
	var current string // current section key, "" when in preamble
	var currentBody []string

	flush := func() {
		if current == "" {
			preamble = append(preamble, currentBody...)
			currentBody = nil
			return
		}
		// Objective is raw text; others are list items.
		if current == "objective" {
			joined := strings.Join(currentBody, " ")
			joined = squeezeWhitespace(joined)
			if joined != "" {
				secs.items[current] = append(secs.items[current], joined)
			}
		} else {
			secs.items[current] = append(secs.items[current], splitListItems(currentBody)...)
		}
		currentBody = nil
	}

	for _, ln := range lines {
		if key, inline, ok := matchHeader(ln); ok {
			flush()
			current = key
			secs.headers[key] = true
			// The remainder of the header line (after "label:") is the start of
			// the body. A common form is "Objective: Build X." on one line.
			if inline != "" {
				currentBody = append(currentBody, inline)
			}
			continue
		}
		currentBody = append(currentBody, ln)
	}
	flush()

	secs.preamble = preamble
	return secs
}

// parsedSections is the intermediate output of parseSections.
type parsedSections struct {
	headers  map[string]bool
	items    map[string][]string
	preamble []string
}

// get returns the first item of a section (for single-valued sections like
// objective) or "" if absent.
func (s parsedSections) get(key string) string {
	items := s.items[key]
	if len(items) == 0 {
		return ""
	}
	return items[0]
}

// list returns the list items of a section, or nil if absent.
func (s parsedSections) list(key string) []string {
	return s.items[key]
}

// has reports whether a section header was encountered.
func (s parsedSections) has(key string) bool { return s.headers[key] }

// knownSectionHeaders maps lowercase canonical labels → section keys. The same
// key may have several aliases.
var knownSectionHeaders = map[string]string{
	"objective":            "objective",
	"goal":                 "objective",
	"acceptance criteria":  "acceptance-criteria",
	"acceptance criterion": "acceptance-criteria",
	"acceptances":          "acceptance-criteria",
	"non-goals":            "non-goals",
	"non goals":            "non-goals",
	"nongoals":             "non-goals",
	"out of scope":         "non-goals",
	"assumptions":          "assumptions",
	"assumption":           "assumptions",
	"constraints":          "constraints",
	"constraint":           "constraints",
	"scope":                "scope",
	"proposed scope":       "scope",
	"risk":                 "risk",
	"complexity":           "complexity",
}

// matchHeader returns the canonical section key (and the inline body that
// follows "label:" on the same line, if any) if the line is a header, else
// ok=false. A header is "<label>:" optionally followed by trailing whitespace
// or a single payload (for the Objective section, payload is appended to body
// on the same line).
func matchHeader(line string) (string, string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return "", "", false
	}
	// Strip a leading markdown header marker ("## Objective:").
	trimmed = strings.TrimLeft(trimmed, "#")
	trimmed = strings.TrimLeft(trimmed, " \t")

	colon := strings.Index(trimmed, ":")
	if colon <= 0 {
		return "", "", false
	}
	label := strings.ToLower(strings.TrimSpace(trimmed[:colon]))
	// A header label has no internal newlines and is short.
	if label == "" || len(label) > 64 {
		return "", "", false
	}
	// Reject labels that contain digits-only or look like a sentence (heuristic:
	// a header label is at most a few words).
	if strings.Contains(label, ".") {
		return "", "", false
	}
	key, ok := knownSectionHeaders[label]
	if !ok {
		return "", "", false
	}
	inline := strings.TrimSpace(trimmed[colon+1:])
	return key, inline, true
}

// normaliseLines splits the description on any line ending (\n, \r\n, \r) and
// returns the resulting lines (without the line terminators). Empty lines are
// preserved (callers need them to detect paragraph boundaries).
func normaliseLines(s string) []string {
	if s == "" {
		return nil
	}
	// Normalise CRLF/CR to LF first so the rest of the parser is line-ending
	// agnostic (regression: TestCompile_Deterministic_AcrossVaryingWhitespace).
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

// splitListItems parses bullet and numbered list items out of the body lines.
// Supported markers: "- ", "* ", "• " and "<n>. " (1..99). A non-list line is
// treated as a separate item (so a one-line "Acceptance Criteria: foo" still
// yields one item).
func splitListItems(body []string) []string {
	var items []string
	var current strings.Builder
	flush := func() {
		s := squeezeWhitespace(current.String())
		if s != "" {
			items = append(items, s)
		}
		current.Reset()
	}
	for _, ln := range body {
		ln = strings.TrimRight(ln, " \t")
		if ln == "" {
			flush()
			continue
		}
		if item, ok := stripListMarker(ln); ok {
			flush()
			if item != "" {
				items = append(items, squeezeWhitespace(item))
			}
			continue
		}
		// Continuation of the previous item (indented) or a free-standing line.
		if current.Len() > 0 {
			current.WriteByte(' ')
			current.WriteString(strings.TrimSpace(ln))
			continue
		}
		current.WriteString(strings.TrimSpace(ln))
	}
	flush()
	return items
}

// stripListMarker returns the body of a bullet/numbered list item and ok=true
// if the line starts with a recognised marker, else ok=false.
func stripListMarker(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return "", false
	}
	// Bullet markers: '-', '*', '•' (U+2022).
	if len(trimmed) >= 3 && trimmed[0] == 0xe2 && trimmed[1] == 0x80 && trimmed[2] == 0xa2 {
		// UTF-8 encoding of '•'; body starts after the marker + whitespace.
		rest := strings.TrimLeft(trimmed[3:], " \t")
		return rest, true
	}
	if len(trimmed) > 0 && (trimmed[0] == '-' || trimmed[0] == '*') {
		if len(trimmed) > 1 && (trimmed[1] == ' ' || trimmed[1] == '\t') {
			return strings.TrimSpace(trimmed[2:]), true
		}
		return "", false
	}
	// Numbered: "<n>. " (1..3 digits).
	dot := strings.IndexByte(trimmed, '.')
	if dot > 0 && dot <= 3 {
		allDigits := true
		for i := 0; i < dot; i++ {
			if trimmed[i] < '0' || trimmed[i] > '9' {
				allDigits = false
				break
			}
		}
		if allDigits && dot+1 < len(trimmed) && (trimmed[dot+1] == ' ' || trimmed[dot+1] == '\t') {
			return strings.TrimSpace(trimmed[dot+2:]), true
		}
	}
	return "", false
}

// squeezeWhitespace collapses runs of whitespace into a single space and trims
// the ends. Used for the objective and list-item text so callers see stable
// strings regardless of source indentation.
func squeezeWhitespace(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	inSpace := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		default:
			b.WriteRune(r)
			inSpace = false
		}
	}
	out := b.String()
	out = strings.Trim(out, " ")
	return out
}

// deriveObjective picks the objective, preferring an explicit "Objective:"
// section, falling back to the title, then to the first non-empty preamble
// paragraph, finally synthesising a placeholder objective from attachment
// metadata. The returned source tells the confidence logic how strong the
// signal was.
func deriveObjective(in CompileInput, secs parsedSections) (string, objectiveSource) {
	if secs.has("objective") {
		if v := secs.get("objective"); v != "" {
			return v, objectiveFromSection
		}
	}
	if in.Title != "" {
		return in.Title, objectiveFromTitle
	}
	if para := firstNonEmptyParagraph(secs.preamble); para != "" {
		return para, objectiveFromDescription
	}
	// Attachment-only path: synthesise from the most informative attachment.
	if obj, ok := synthesiseObjectiveFromAttachments(in); ok {
		return obj, objectiveFromAttachment
	}
	return "", objectiveAbsent
}

type objectiveSource int

const (
	objectiveAbsent objectiveSource = iota
	objectiveFromAttachment
	objectiveFromDescription
	objectiveFromTitle
	objectiveFromSection
)

// firstNonEmptyParagraph returns the first paragraph (one or more consecutive
// non-empty lines) from the preamble, with internal whitespace collapsed.
func firstNonEmptyParagraph(preamble []string) string {
	var collected []string
	for _, ln := range preamble {
		if strings.TrimSpace(ln) == "" {
			if len(collected) > 0 {
				break
			}
			continue
		}
		collected = append(collected, strings.TrimSpace(ln))
	}
	if len(collected) == 0 {
		return ""
	}
	return squeezeWhitespace(strings.Join(collected, " "))
}

// synthesiseObjectiveFromAttachments builds a placeholder objective from the
// attachment metadata when no description is provided (spec §9.2: an attachment
// alone is a valid task input). The compiler does not read attachment content;
// the placeholder is advisory and must always be paired with LOW confidence +
// a clarification.
func synthesiseObjectiveFromAttachments(in CompileInput) (string, bool) {
	if len(in.Attachments) == 0 {
		return "", false
	}
	// Pick the most informative attachment: REQUIREMENTS > API_SPECIFICATION >
	// DESIGN_REFERENCE > others.
	priority := func(r AttachmentRole) int {
		switch r {
		case RoleRequirements:
			return 5
		case RoleAPISpec:
			return 4
		case RoleDesignReference:
			return 3
		case RoleExample:
			return 2
		case RoleBugScreenshot:
			return 1
		}
		return 0
	}
	best := 0
	for i, a := range in.Attachments {
		if priority(a.Role) > priority(in.Attachments[best].Role) {
			best = i
		}
	}
	a := in.Attachments[best]
	roleLabel := strings.ToLower(strings.ReplaceAll(string(a.Role), "_", " "))
	return fmt.Sprintf("Implement changes based on the attached %s (%s).",
		roleLabel, a.Filename), true
}

// deriveAcceptanceCriteria returns the explicit AC list if present, else
// synthesises a single default AC from the objective so the resulting spec
// still validates (ValidateSpecification requires ≥1 AC). The synthesised flag
// tells the caller a safe assumption was made (§9.7).
func deriveAcceptanceCriteria(secs parsedSections, objective string) ([]AcceptanceCriterion, bool) {
	if secs.has("acceptance-criteria") {
		items := secs.list("acceptance-criteria")
		if len(items) > 0 {
			acs := make([]AcceptanceCriterion, 0, len(items))
			for i, stmt := range items {
				if strings.TrimSpace(stmt) == "" {
					continue
				}
				acs = append(acs, AcceptanceCriterion{
					ID:        fmt.Sprintf("AC-%d", i+1),
					Statement: stmt,
				})
			}
			if len(acs) > 0 {
				return acs, false
			}
		}
	}
	if objective == "" {
		return nil, true
	}
	return []AcceptanceCriterion{{
		ID:        "AC-1",
		Statement: fmt.Sprintf("The change satisfies its objective: %s", objective),
	}}, true
}

// classifyRisk maps the input through the deterministic §26 classifier in
// internal/risk. The classifier never calls an LLM; it scans description text
// and attachment filenames for structural + lexical signals.
func classifyRisk(in CompileInput) risk.Result {
	var paths []string
	for _, a := range in.Attachments {
		paths = append(paths, a.Filename)
	}
	signals := risk.Signals{
		Description:         joinForClassifier(in),
		Paths:               paths,
		TouchesAuth:         containsAnyFold(in.Description, "auth", "oauth", "sso", "2fa", "mfa", "session token"),
		TouchesPayments:     containsAnyFold(in.Description, "payment", "billing", "charge", "refund", "stripe"),
		TouchesPermissions:  containsAnyFold(in.Description, "permission", "acl", "authorization", "rbac"),
		DestructiveCommands: containsAnyFold(in.Description, "drop table", "rm -rf", "force push", "delete user"),
		HasMigrations:       containsAnyFold(in.Description, "migration", "migrate", "schema change", "alter table"),
		ConcurrencyChange:   containsAnyFold(in.Description, "race", "deadlock", "mutex", "lock", "transaction"),
		SubscriptionChange:  containsAnyFold(in.Description, "subscription", "subscribe", "billing plan"),
		PublicAPIChange:     containsAnyFold(in.Description, "public api", "api endpoint", "webhook", "openapi"),
		TouchesInfra:        containsAnyFold(in.Description, "ci/", "infrastructure", "terraform", "deployment"),
	}
	return risk.Classify(signals)
}

// joinForClassifier builds the description blob the classifier scans: the
// free-form description plus the title (the title often carries risk keywords
// like "Rotate OAuth secrets").
func joinForClassifier(in CompileInput) string {
	parts := make([]string, 0, 2)
	if in.Title != "" {
		parts = append(parts, in.Title)
	}
	if in.Description != "" {
		parts = append(parts, in.Description)
	}
	return strings.Join(parts, "\n")
}

// toTaskRisk translates the risk package's level (R0..R4) into the M14-01
// task.Risk type. They share the same identifiers but live in separate packages
// (data-shape type vs classifier-internal type).
func toTaskRisk(l risk.Level) Risk {
	switch l {
	case risk.R0:
		return RiskR0
	case risk.R1:
		return RiskR1
	case risk.R2:
		return RiskR2
	case risk.R3:
		return RiskR3
	case risk.R4:
		return RiskR4
	}
	return ""
}

// complexityResult is the local deterministic complexity classifier output.
type complexityResult struct {
	Complexity Complexity
	Reasons    []string
}

// classifyComplexity maps input signals onto the §18.2/§19.3 bands
// (C0..C3) deterministically. The mapping is intentionally simpler than the
// router's C0..C4 model because the M14-01 Specification model only has four
// bands (C3 is the highest; anything heavier is capped to C3 and surfaced as
// an escalation recommendation via Clarifications).
func classifyComplexity(in CompileInput, secs parsedSections) complexityResult {
	low := strings.ToLower(joinForClassifier(in))
	score := 0
	var reasons []string

	if containsAny(low, "typo", "rename", "docs", "changelog", "comment", "format", "lint") {
		reasons = append(reasons, "mechanical/docs change")
	} else {
		score += 1
	}
	if containsAny(low, "refactor", "feature", "implement", "build") {
		score += 1
		reasons = append(reasons, "implementation/refactor role")
	}
	if containsAny(low, "migration", "migrate", "schema") {
		score += 1
		reasons = append(reasons, "migration/schema role")
	}
	if containsAny(low, "architect", "design system", "rewrite", "overhaul") {
		score += 2
		reasons = append(reasons, "architectural role")
	}
	if len(secs.list("scope")) >= 5 {
		score += 1
		reasons = append(reasons, "large proposed scope (>=5 items)")
	}
	if len(in.Attachments) >= 3 {
		score += 1
		reasons = append(reasons, "multiple attachments (cross-cutting context)")
	}
	// A risky input is at least C2: an R3/R4 task cannot be a trivial C0.
	if containsAny(low, "migration", "concurrency", "auth", "payment", "permission", "subscription") {
		if score < 2 {
			score = 2
			reasons = append(reasons, "elevated-risk topic floors complexity at C2")
		}
	}
	switch {
	case score >= 4:
		return complexityResult{Complexity: ComplexityC3, Reasons: reasons}
	case score >= 3:
		return complexityResult{Complexity: ComplexityC2, Reasons: reasons}
	case score >= 1:
		return complexityResult{Complexity: ComplexityC1, Reasons: reasons}
	default:
		return complexityResult{Complexity: ComplexityC0, Reasons: reasons}
	}
}

// deriveVisualRequirements sets Required=true iff the task includes a
// DESIGN_REFERENCE or BUG_SCREENSHOT attachment (the only roles that imply
// visual verification per §15/§16). The hashes are propagated as References
// so the design pipeline can pick them up later.
func deriveVisualRequirements(attachments []Attachment) VisualRequirements {
	vr := VisualRequirements{}
	for _, a := range attachments {
		switch a.Role {
		case RoleDesignReference, RoleBugScreenshot:
			vr.Required = true
			vr.References = append(vr.References, a.Hash)
		}
	}
	return vr
}

// deriveConfidenceAndClarifications applies the §9.7 rules: pick a safe
// assumption whenever possible; only emit a Clarification when no safe
// assumption exists.
func deriveConfidenceAndClarifications(
	in CompileInput,
	objSource objectiveSource,
	acsSynthesised bool,
	spec Specification,
	riskRes risk.Result,
) (Confidence, []Clarification) {
	var clarifications []Clarification

	// Vague input: empty description and no attachment → cannot formulate an
	// actionable objective.
	if objSource == objectiveAbsent {
		clarifications = append(clarifications, Clarification{
			Question: "What is the objective of this task?",
			Reason:   "The input had no description and no attachment; the compiler cannot formulate an actionable objective.",
		})
		return ConfidenceLow, clarifications
	}

	// Attachment-only task: the compiler cannot read attachment content, so the
	// objective is a placeholder. Always ask the user to confirm.
	if objSource == objectiveFromAttachment {
		clarifications = append(clarifications, Clarification{
			Question: "Confirm the objective derived from the attachment.",
			Reason:   "The compiler does not read attachment content; the placeholder objective is advisory.",
			Options:  []string{"Confirm the placeholder", "Provide an explicit objective"},
		})
		return ConfidenceLow, clarifications
	}

	// R4 (auth/payment/permissions/destructive) is unsafe to silently assume:
	// the variants lead to materially different products (§9.7). Surface a
	// clarification even when the input is otherwise well-structured.
	if riskRes.Level == risk.R4 {
		clarifications = append(clarifications, Clarification{
			Question: "Confirm the security/money impact and review posture.",
			Reason:   "R4 changes (auth/payments/permissions/destructive) carry an elevated review requirement; a safe assumption is not possible.",
			Options:  []string{"LOCAL_REVIEW with explicit approval", "REMOTE_REVIEW", "AUTONOMOUS with revert enabled"},
		})
	}

	// One-word / extremely short free-form input is too vague to act on without
	// confirmation, even if it produced an objective. The check considers both
	// the description and the title: a 6-character "fix it" title does not
	// rescue a 6-character description.
	if objSource == objectiveFromDescription || objSource == objectiveFromTitle {
		blob := squeezeWhitespace(in.Title + " " + in.Description)
		if len(blob) < 16 {
			clarifications = append(clarifications, Clarification{
				Question: "Provide more detail about the expected change.",
				Reason:   "The free-form input (title + description) is shorter than 16 characters; the compiler cannot infer acceptance criteria or scope.",
			})
			return ConfidenceLow, clarifications
		}
	}

	// HIGH only when the user gave explicit structured sections for objective
	// AND acceptance criteria. Anything else is at most MEDIUM.
	if objSource == objectiveFromSection && !acsSynthesised && len(clarifications) == 0 {
		return ConfidenceHigh, nil
	}

	// Default: MEDIUM. The compiler made safe assumptions; clarifications (if
	// any) are advisory, not blocking.
	if len(clarifications) > 0 {
		// We have an R4 clarification but otherwise actionable input. This is
		// MEDIUM (actionable with caveats), not LOW.
		return ConfidenceMedium, clarifications
	}
	return ConfidenceMedium, nil
}

// collectAttachmentRoles builds the hash→role map the compiler surfaces as
// AttachmentRoles. The compiler does not invent roles; it mirrors the metadata
// it was given (§18.1).
func collectAttachmentRoles(attachments []Attachment) map[string]AttachmentRole {
	if len(attachments) == 0 {
		return nil
	}
	out := make(map[string]AttachmentRole, len(attachments))
	for _, a := range attachments {
		out[a.Hash] = a.Role
	}
	return out
}

// containsAnyFold reports whether s contains any of subs (case-insensitive).
func containsAnyFold(s string, subs ...string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		if strings.Contains(low, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
