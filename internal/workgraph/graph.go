// This file implements the work-graph domain model (spec §18.3): work packages,
// stages, dependencies, package states, attempts, AC→scope mapping, deterministic
// decomposition, and DAG validation. It is deliberately disconnected from
// execution (no daemon, no scheduler, no storage): M14-04 delivers only the
// domain model + its invariants. Wiring into the dispatch/scheduler pipeline is
// a later milestone.
//
// Design notes:
//
//   - "Parse, don't validate." A ValidatedWorkGraph is only constructible through
//     ValidateWorkGraph, so any holder of a *ValidatedWorkGraph has a proof that
//     the DAG invariants hold. A future persistence layer will accept only a
//     ValidatedWorkGraph, making it impossible to persist an invalid DAG as
//     runnable (baseline: "Unimplemented requirements must be explicitly marked,
//     never disguised as stubs that look finished" — the runnable handle is the
//     type, not a flag).
//
//   - Decompose is a pure function of task.Specification: no I/O, no clock, no
//     randomness. Identical specifications produce byte-identical graphs
//     (mirrors the M14-02 task.Compile determinism contract).
//
//   - AC ownership is strict: every package owns ≥1 acceptance criterion and
//     every acceptance criterion is owned by at most one package. This makes
//     "duplicate AC owner" a crisp, detectable defect.
//
//   - Weak connectivity is required: a work graph is one cohesive unit for one
//     task, so a disconnected component (an "unreachable" package) is rejected.

package workgraph

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"neuroforge/internal/task"
)

// Stage is a phase of the work pipeline (spec §18.3:
// research → contract → implementation → integration → verification).
type Stage string

const (
	StageResearch       Stage = "research"
	StageContract       Stage = "contract"
	StageImplementation Stage = "implementation"
	StageIntegration    Stage = "integration"
	StageVerification   Stage = "verification"
)

// AllStages is the complete ordered set of pipeline stages.
var AllStages = []Stage{
	StageResearch,
	StageContract,
	StageImplementation,
	StageIntegration,
	StageVerification,
}

// IsValid reports whether s is a known pipeline stage.
func (s Stage) IsValid() bool {
	for _, x := range AllStages {
		if x == s {
			return true
		}
	}
	return false
}

// PackageState is the lifecycle state of a work package.
type PackageState string

const (
	// PackagePending: created but not yet ready (e.g. dependencies unresolved).
	PackagePending PackageState = "pending"
	// PackageReady: dependencies satisfied; eligible to be dispatched.
	PackageReady PackageState = "ready"
	// PackageRunning: an attempt is in flight.
	PackageRunning PackageState = "running"
	// PackageBlocked: cannot proceed (lease conflict, quota, manual hold).
	PackageBlocked PackageState = "blocked"
	// PackageSucceeded: completed successfully.
	PackageSucceeded PackageState = "succeeded"
	// PackageFailed: all attempts exhausted without success.
	PackageFailed PackageState = "failed"
	// PackageSkipped: deliberately not executed (e.g. AC dropped).
	PackageSkipped PackageState = "skipped"
)

// AllPackageStates is the complete set of package states.
var AllPackageStates = []PackageState{
	PackagePending,
	PackageReady,
	PackageRunning,
	PackageBlocked,
	PackageSucceeded,
	PackageFailed,
	PackageSkipped,
}

// IsValid reports whether s is a known package state.
func (s PackageState) IsValid() bool {
	for _, x := range AllPackageStates {
		if x == s {
			return true
		}
	}
	return false
}

// IsTerminal reports whether s is a terminal state (no further transitions).
func (s PackageState) IsTerminal() bool {
	switch s {
	case PackageSucceeded, PackageFailed, PackageSkipped:
		return true
	}
	return false
}

// Attempt is one execution attempt of a work package. Attempts are append-only
// history records; the package's current State is the authoritative lifecycle
// handle. An empty Attempts slice means the package has not yet been dispatched.
type Attempt struct {
	Index         int          `json:"index"`
	State         PackageState `json:"state"`
	StartedAt     time.Time    `json:"started_at"`
	FinishedAt    time.Time    `json:"finished_at,omitempty"`
	FailureReason string       `json:"failure_reason,omitempty"`
	ExitCode      int          `json:"exit_code,omitempty"`
	AgentRunID    string       `json:"agent_run_id,omitempty"`
}

// WorkPackage is a unit of work in the DAG. It owns one or more acceptance
// criteria (the "package → AC → allowed scope" mapping) and depends on zero or
// more predecessor packages that must complete first.
type WorkPackage struct {
	// ID is the stable package identifier. For graphs produced by Decompose it
	// is derived deterministically from (TaskID, AC ID); for hand-built graphs
	// it is any non-empty string unique within the graph.
	ID string `json:"id"`
	// TaskID is the owning task's ID (spec §17.2 path component).
	TaskID string `json:"task_id"`
	// Stage is the pipeline phase (spec §18.3).
	Stage Stage `json:"stage"`
	// Title is a short human-readable label.
	Title string `json:"title"`
	// Objective links this package to the task objective (brief AC: "every
	// package is linked to an objective/AC"). Non-empty.
	Objective string `json:"objective"`
	// AcceptedACIDs are the acceptance-criterion IDs (spec §27, e.g. "AC-1")
	// this package is responsible for delivering. Must be ≥1; each ID is owned
	// by at most one package across the whole graph. When the graph is derived
	// from a task.Specification, every ID must exist in that specification's
	// AcceptanceCriteria and every specification AC must be covered exactly once.
	AcceptedACIDs []string `json:"accepted_ac_ids"`
	// AllowedScope is the set of proposed-scope items / file path prefixes this
	// package may touch (the "AC → allowed scope" half of the mapping). It is an
	// advisory allowlist enforced downstream by the workspace/lease layer, not a
	// hard boundary at the domain layer.
	AllowedScope []string `json:"allowed_scope,omitempty"`
	// Dependencies are the IDs of packages that must reach a terminal
	// SUCCEEDED state before this package may start. Each must reference an
	// existing package ID in the same graph; cycles are forbidden.
	Dependencies []string `json:"dependencies,omitempty"`
	// State is the current lifecycle state.
	State PackageState `json:"state"`
	// Attempts is the append-only execution history.
	Attempts []Attempt `json:"attempts,omitempty"`
}

// WorkGraph is the mutable, pre-validation container of work packages for one
// task. It carries no invariant guarantees; use ValidateWorkGraph to obtain a
// ValidatedWorkGraph before persisting or dispatching.
type WorkGraph struct {
	TaskID   string        `json:"task_id"`
	Packages []WorkPackage `json:"packages"`
}

// ValidatedWorkGraph is a WorkGraph that has passed ValidateWorkGraph. It is the
// only "runnable" handle: construction is only possible through the validator,
// so any code path that holds a *ValidatedWorkGraph has a structural proof that
// the DAG invariants hold. A future persistence layer will accept only this
// type, making it impossible to persist an invalid DAG as runnable.
type ValidatedWorkGraph struct {
	graph WorkGraph
}

// Graph returns the underlying work graph. The returned value is a deep copy of
// the validated packages, so callers cannot mutate the validated state.
func (v *ValidatedWorkGraph) Graph() WorkGraph {
	return v.graph.clone()
}

// TaskID returns the owning task's ID.
func (v *ValidatedWorkGraph) TaskID() string { return v.graph.TaskID }

// Packages returns the validated packages in canonical (sorted-by-ID) order.
// The returned slice is a defensive copy.
func (v *ValidatedWorkGraph) Packages() []WorkPackage {
	out := make([]WorkPackage, len(v.graph.Packages))
	copy(out, v.graph.Packages)
	return out
}

// PackageForAC returns the package that owns the given acceptance-criterion ID,
// or false if no package owns it. (AC ownership is unique by construction.)
func (v *ValidatedWorkGraph) PackageForAC(acID string) (WorkPackage, bool) {
	for _, p := range v.graph.Packages {
		for _, id := range p.AcceptedACIDs {
			if id == acID {
				return p, true
			}
		}
	}
	return WorkPackage{}, false
}

// TopologicalOrder returns the package IDs in a dependency-respecting order
// (dependencies before dependents). Deterministic: ties break by package ID.
// Returns an error only if the validated graph is somehow cyclic (should be
// impossible by construction; the check is defense-in-depth).
func (v *ValidatedWorkGraph) TopologicalOrder() ([]string, error) {
	return topologicalOrder(v.graph)
}

// clone returns a deep copy of the graph (slices duplicated).
func (g WorkGraph) clone() WorkGraph {
	out := WorkGraph{TaskID: g.TaskID, Packages: make([]WorkPackage, len(g.Packages))}
	for i, p := range g.Packages {
		out.Packages[i] = p.clone()
	}
	return out
}

func (p WorkPackage) clone() WorkPackage {
	out := p
	if p.AcceptedACIDs != nil {
		out.AcceptedACIDs = append([]string(nil), p.AcceptedACIDs...)
	}
	if p.AllowedScope != nil {
		out.AllowedScope = append([]string(nil), p.AllowedScope...)
	}
	if p.Dependencies != nil {
		out.Dependencies = append([]string(nil), p.Dependencies...)
	}
	if p.Attempts != nil {
		out.Attempts = append([]Attempt(nil), p.Attempts...)
	}
	return out
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrInvalidWorkGraph is returned by ValidateWorkGraph when one or more
// invariants are violated. The wrapped errors carry the specific violations.
var ErrInvalidWorkGraph = errors.New("invalid work graph")

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidateWorkGraph checks the structural DAG invariants of g and returns a
// ValidatedWorkGraph (the runnable handle) on success. It does NOT touch
// storage and does NOT require a task.Specification: it validates the graph's
// internal consistency only. Use ValidateAgainstSpec (or Decompose) to also
// enforce that every owned AC exists in a specification and every specification
// AC is covered.
//
// Invariants enforced:
//
//   - TaskID is non-empty.
//   - At least one package.
//   - Every package: non-empty ID, valid Stage, non-empty Title, non-empty
//     Objective, ≥1 AcceptedACID, valid State.
//   - Package IDs are unique.
//   - Every Dependency references an existing package ID (no missing edges).
//   - No dependency cycle (a self-dependency is a cycle).
//   - Each acceptance-criterion ID is owned by at most one package (no duplicate
//     AC owner).
//   - The graph is weakly connected (no unreachable / orphan package).
func ValidateWorkGraph(g WorkGraph) (*ValidatedWorkGraph, error) {
	normalised, err := validate(g, nil)
	if err != nil {
		return nil, err
	}
	return &ValidatedWorkGraph{graph: normalised}, nil
}

// ValidateAgainstSpec validates g and additionally enforces the mapping between
// the graph and a compiled task.Specification:
//
//   - Every package's owned AC ID must exist in spec.AcceptanceCriteria.
//   - Every AC in spec.AcceptanceCriteria must be owned by exactly one package
//     (full coverage; combined with the no-duplicate rule this is "exactly one").
//
// The specification is validated first via task.ValidateSpecification so an
// invalid specification fails fast with a clear error.
func ValidateAgainstSpec(g WorkGraph, spec task.Specification) (*ValidatedWorkGraph, error) {
	spec, err := task.ValidateSpecification(spec)
	if err != nil {
		return nil, fmt.Errorf("%w: specification is invalid: %w", ErrInvalidWorkGraph, err)
	}
	normalised, err := validate(g, &spec)
	if err != nil {
		return nil, err
	}
	return &ValidatedWorkGraph{graph: normalised}, nil
}

func validate(g WorkGraph, spec *task.Specification) (WorkGraph, error) {
	var errs []error

	if strings.TrimSpace(g.TaskID) == "" {
		errs = append(errs, fmt.Errorf("%w: task_id is required", ErrInvalidWorkGraph))
	}
	if len(g.Packages) == 0 {
		errs = append(errs, fmt.Errorf("%w: at least one work package is required", ErrInvalidWorkGraph))
	}

	// Per-package structural checks + ID uniqueness.
	ids := make(map[string]int, len(g.Packages)) // id -> first-seen index
	acOwners := make(map[string]string)          // acID -> owning package ID
	for i := range g.Packages {
		p := &g.Packages[i]
		p.ID = strings.TrimSpace(p.ID)
		p.TaskID = strings.TrimSpace(p.TaskID)
		p.Title = strings.TrimSpace(p.Title)
		p.Objective = strings.TrimSpace(p.Objective)
		p.Stage = Stage(strings.TrimSpace(string(p.Stage)))

		if p.ID == "" {
			errs = append(errs, fmt.Errorf("%w: package #%d has no id", ErrInvalidWorkGraph, i+1))
		} else if prev, dup := ids[p.ID]; dup {
			errs = append(errs, fmt.Errorf("%w: duplicate package id %q (packages #%d and #%d)", ErrInvalidWorkGraph, p.ID, prev+1, i+1))
		} else {
			ids[p.ID] = i
		}

		if p.TaskID == "" {
			errs = append(errs, fmt.Errorf("%w: package %q has no task_id", ErrInvalidWorkGraph, p.ID))
		}
		if !p.Stage.IsValid() {
			errs = append(errs, fmt.Errorf("%w: package %q has unknown stage %q", ErrInvalidWorkGraph, p.ID, p.Stage))
		}
		if p.Title == "" {
			errs = append(errs, fmt.Errorf("%w: package %q has no title", ErrInvalidWorkGraph, p.ID))
		}
		if p.Objective == "" {
			errs = append(errs, fmt.Errorf("%w: package %q has no objective", ErrInvalidWorkGraph, p.ID))
		}
		if !p.State.IsValid() {
			errs = append(errs, fmt.Errorf("%w: package %q has unknown state %q", ErrInvalidWorkGraph, p.ID, p.State))
		}
		if p.State == "" {
			// Default to pending when unspecified; treat empty as a request for
			// the default rather than an error, but record a normalised value.
			p.State = PackagePending
		}

		// Trim + de-duplicate the AC list within the package.
		p.AcceptedACIDs = trimAndDedupStrings(p.AcceptedACIDs)
		if len(p.AcceptedACIDs) == 0 {
			errs = append(errs, fmt.Errorf("%w: package %q owns no acceptance criterion (every package must be linked to ≥1 AC)", ErrInvalidWorkGraph, p.ID))
		}
		for _, ac := range p.AcceptedACIDs {
			if owner, exists := acOwners[ac]; exists {
				errs = append(errs, fmt.Errorf("%w: duplicate AC owner: acceptance criterion %q is owned by both %q and %q", ErrInvalidWorkGraph, ac, owner, p.ID))
			} else {
				acOwners[ac] = p.ID
			}
		}

		// Trim dependencies + allowed scope.
		p.Dependencies = trimAndDedupStrings(p.Dependencies)
		p.AllowedScope = trimAndDedupStrings(p.AllowedScope)
	}

	// Missing-edge check: every dependency must reference an existing package.
	for i := range g.Packages {
		p := &g.Packages[i]
		for _, dep := range p.Dependencies {
			if dep == p.ID {
				errs = append(errs, fmt.Errorf("%w: package %q depends on itself (self-cycle)", ErrInvalidWorkGraph, p.ID))
				continue
			}
			if _, ok := ids[dep]; !ok {
				errs = append(errs, fmt.Errorf("%w: package %q depends on missing package %q", ErrInvalidWorkGraph, p.ID, dep))
			}
		}
	}

	// Cycle check (DFS 3-colour). Only meaningful when ids are unique + deps
	// exist; we run it regardless because it also surfaces self-loops via the
	// adjacency walk.
	if cycErr := detectCycle(g); cycErr != nil {
		errs = append(errs, cycErr)
	}

	// Weak-connectivity check (no unreachable / orphan package). Skipped when
	// the graph is empty or has structural errors that make adjacency invalid.
	if len(g.Packages) > 0 && len(ids) == len(g.Packages) {
		if unreachable := weaklyUnreachable(g); len(unreachable) > 0 {
			sort.Strings(unreachable)
			errs = append(errs, fmt.Errorf("%w: unreachable packages (not weakly-connected to package #1): %v", ErrInvalidWorkGraph, unreachable))
		}
	}

	// Spec-mapping checks (only when a specification is provided).
	if spec != nil {
		specACs := make(map[string]task.AcceptanceCriterion, len(spec.AcceptanceCriteria))
		for _, ac := range spec.AcceptanceCriteria {
			specACs[ac.ID] = ac
		}
		// Every owned AC must exist in the spec.
		for ac, owner := range acOwners {
			if _, ok := specACs[ac]; !ok {
				errs = append(errs, fmt.Errorf("%w: package %q owns acceptance criterion %q which is not in the specification", ErrInvalidWorkGraph, owner, ac))
			}
		}
		// Every spec AC must be covered exactly once (no duplicate is already
		// checked above; here we check "at least once").
		for _, ac := range spec.AcceptanceCriteria {
			if _, ok := acOwners[ac.ID]; !ok {
				errs = append(errs, fmt.Errorf("%w: acceptance criterion %q is not owned by any package (coverage gap)", ErrInvalidWorkGraph, ac.ID))
			}
		}
	}

	if len(errs) > 0 {
		return g, errors.Join(errs...)
	}

	// Canonicalise package order (sorted by ID) so the validated graph is
	// deterministic regardless of input order.
	sort.SliceStable(g.Packages, func(i, j int) bool {
		return g.Packages[i].ID < g.Packages[j].ID
	})
	return g, nil
}

func trimAndDedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// detectCycle returns a non-nil error describing a dependency cycle if one
// exists, using DFS 3-colouring. The cycle is reported as an ordered path.
func detectCycle(g WorkGraph) error {
	adj := make(map[string][]string, len(g.Packages))
	for _, p := range g.Packages {
		adj[p.ID] = append([]string(nil), p.Dependencies...)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(g.Packages))
	// Iterate in a deterministic order so the reported cycle path is stable.
	ids := make([]string, 0, len(g.Packages))
	for _, p := range g.Packages {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)

	var stack []string
	var dfs func(node string) []string
	dfs = func(node string) []string {
		color[node] = gray
		stack = append(stack, node)
		deps := append([]string(nil), adj[node]...)
		sort.Strings(deps)
		for _, d := range deps {
			if color[d] == gray {
				// Found a cycle: d is on the current stack. Slice from d.
				for i, n := range stack {
					if n == d {
						cyc := append([]string(nil), stack[i:]...)
						cyc = append(cyc, d) // close the loop for readability
						return cyc
					}
				}
				return []string{d, node, d}
			}
			if color[d] == white {
				if found := dfs(d); found != nil {
					return found
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[node] = black
		return nil
	}

	for _, id := range ids {
		if color[id] == white {
			if cyc := dfs(id); cyc != nil {
				return fmt.Errorf("%w: dependency cycle: %s", ErrInvalidWorkGraph, strings.Join(cyc, " -> "))
			}
		}
	}
	return nil
}

// weaklyUnreachable returns the package IDs that are NOT weakly-connected to
// the first package (by input order). A non-empty result means the graph has
// more than one weakly-connected component. The first package itself is always
// considered reachable.
func weaklyUnreachable(g WorkGraph) []string {
	if len(g.Packages) == 0 {
		return nil
	}
	// Build undirected adjacency.
	adj := make(map[string]map[string]bool, len(g.Packages))
	add := func(a, b string) {
		if adj[a] == nil {
			adj[a] = make(map[string]bool)
		}
		adj[a][b] = true
	}
	present := make(map[string]bool, len(g.Packages))
	for _, p := range g.Packages {
		present[p.ID] = true
	}
	for _, p := range g.Packages {
		for _, d := range p.Dependencies {
			if present[d] {
				add(p.ID, d)
				add(d, p.ID)
			}
		}
	}

	root := g.Packages[0].ID
	visited := make(map[string]bool, len(g.Packages))
	queue := []string{root}
	visited[root] = true
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for n := range adj[cur] {
			if !visited[n] {
				visited[n] = true
				queue = append(queue, n)
			}
		}
	}
	var unreachable []string
	for _, p := range g.Packages {
		if !visited[p.ID] {
			unreachable = append(unreachable, p.ID)
		}
	}
	return unreachable
}

// topologicalOrder returns package IDs in dependency-respecting order
// (dependencies before dependents), with deterministic tie-breaking by ID.
func topologicalOrder(g WorkGraph) ([]string, error) {
	adj := make(map[string][]string, len(g.Packages)) // dep -> dependents
	inDeg := make(map[string]int, len(g.Packages))    // package -> #unmet deps
	present := make(map[string]bool, len(g.Packages))
	for _, p := range g.Packages {
		present[p.ID] = true
		inDeg[p.ID] = 0
	}
	for _, p := range g.Packages {
		for _, d := range p.Dependencies {
			if !present[d] {
				return nil, fmt.Errorf("%w: package %q depends on missing package %q", ErrInvalidWorkGraph, p.ID, d)
			}
			adj[d] = append(adj[d], p.ID)
			inDeg[p.ID]++
		}
	}
	// Kahn's algorithm with deterministic (sorted) selection.
	var ready []string
	for id, deg := range inDeg {
		if deg == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	out := make([]string, 0, len(g.Packages))
	for len(ready) > 0 {
		cur := ready[0]
		ready = ready[1:]
		out = append(out, cur)
		next := append([]string(nil), adj[cur]...)
		sort.Strings(next)
		for _, n := range next {
			inDeg[n]--
			if inDeg[n] == 0 {
				// Insert in sorted position to keep `ready` ordered.
				idx := sort.SearchStrings(ready, n)
				ready = append(ready, "")
				copy(ready[idx+1:], ready[idx:])
				ready[idx] = n
			}
		}
	}
	if len(out) != len(g.Packages) {
		return nil, fmt.Errorf("%w: topological sort failed (cycle exists)", ErrInvalidWorkGraph)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Deterministic decomposition
// ---------------------------------------------------------------------------

// Decompose builds a deterministic ValidatedWorkGraph from a compiled task
// Specification (spec §18.1/§18.3). It is a pure function: no I/O, no clock, no
// randomness — identical specifications produce byte-identical graphs (mirrors
// the M14-02 task.Compile determinism contract).
//
// Decomposition strategy (minimal, honest, deterministic):
//
//   - One implementation package per acceptance criterion (AC order is fixed by
//     the specification, which the compiler appends in stable input order).
//     Each package owns exactly one AC and is linked to the specification's
//     Objective.
//   - Packages are chained sequentially (AC-2's package depends on AC-1's,
//     AC-3's on AC-2's, …). This is the safe default in the absence of explicit
//     independence information: it makes no parallelism assumption and keeps the
//     graph a single weakly-connected component. A future decomposition
//     improvement (FU-M14-04-1) may relax this once role/stage hints exist.
//   - AllowedScope: every package receives the specification's full
//     ProposedScope list (conservative over-approximation; finer scope
//     partitioning is execution-time and out of scope for M14-04).
//
// The returned graph is validated against the specification, so a malformed
// specification (empty objective, no ACs, duplicate AC IDs) surfaces as a
// validation error rather than producing an invalid graph.
func Decompose(spec task.Specification) (*ValidatedWorkGraph, error) {
	spec, err := task.ValidateSpecification(spec)
	if err != nil {
		return nil, fmt.Errorf("%w: specification is invalid: %w", ErrInvalidWorkGraph, err)
	}

	packages := make([]WorkPackage, 0, len(spec.AcceptanceCriteria))
	for i, ac := range spec.AcceptanceCriteria {
		pkg := WorkPackage{
			ID:            packageID(spec.TaskID, ac.ID),
			TaskID:        spec.TaskID,
			Stage:         StageImplementation,
			Title:         packageTitle(ac),
			Objective:     spec.Objective,
			AcceptedACIDs: []string{ac.ID},
			AllowedScope:  append([]string(nil), spec.ProposedScope...),
			Dependencies:  nil,
			State:         PackagePending,
		}
		if i > 0 {
			prev := spec.AcceptanceCriteria[i-1]
			pkg.Dependencies = []string{packageID(spec.TaskID, prev.ID)}
		}
		packages = append(packages, pkg)
	}

	g := WorkGraph{TaskID: spec.TaskID, Packages: packages}
	return ValidateAgainstSpec(g, spec)
}

// packageID is the deterministic package identifier derived from (taskID, acID).
// It is stable across re-decompositions of the same specification.
func packageID(taskID, acID string) string {
	return taskID + "-" + acID
}

// packageTitle is the deterministic human-readable label for an AC-owning
// implementation package produced by Decompose.
func packageTitle(ac task.AcceptanceCriterion) string {
	stmt := strings.TrimSpace(ac.Statement)
	if stmt == "" {
		return "Implement " + ac.ID
	}
	if len(stmt) > 80 {
		stmt = stmt[:77] + "..."
	}
	return "Implement " + ac.ID + ": " + stmt
}
