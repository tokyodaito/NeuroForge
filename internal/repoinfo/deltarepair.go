package repoinfo

import (
	"strings"
)

// DeltaRepairContext is the targeted context handed to a repair agent (spec
// §22.5). It contains ONLY:
//   - the finding that triggered the repair;
//   - the current diff;
//   - the failing test (if any);
//   - the specific files implicated.
//
// It deliberately does NOT replay the full research history of the run
// (§22.5: "Repair agent does NOT receive the full research history again").
type DeltaRepairContext struct {
	Finding         string
	Severity        string
	Diff            string
	FailingTest     string
	FailingTestLog  string // already a LogSlice.Render() — never the full log
	ImplicatedFiles []FileSlice
	NextObjective   string
	EstimatedTokens int
}

// DeltaOptions controls assembly of a delta repair context.
type DeltaOptions struct {
	Finding        string
	Severity       string
	Diff           string
	FailingTest    string
	FailingTestLog string // already sliced
	// ImplicatedPaths are the files the repair agent may need; the index slices
	// them (trimmed) into the context.
	ImplicatedPaths []string
	NextObjective   string
	Budget          int
}

// DefaultDeltaBudget caps a delta repair context (§22.5 keeps it small).
const DefaultDeltaBudget = 6000

// AssembleDelta builds a compact delta repair context from the index (§22.5).
// It never includes the full conversation history.
func (idx *Index) AssembleDelta(opts DeltaOptions) (*DeltaRepairContext, error) {
	if opts.Budget <= 0 {
		opts.Budget = DefaultDeltaBudget
	}
	d := &DeltaRepairContext{
		Finding:        opts.Finding,
		Severity:       opts.Severity,
		Diff:           opts.Diff,
		FailingTest:    opts.FailingTest,
		FailingTestLog: opts.FailingTestLog,
		NextObjective:  opts.NextObjective,
	}
	core := d.coreBlock()
	d.EstimatedTokens += EstimateTokens(core)
	if d.EstimatedTokens > opts.Budget {
		return d, nil
	}
	remaining := opts.Budget - d.EstimatedTokens
	for _, p := range opts.ImplicatedPaths {
		fe := idx.ByPath[p]
		if fe == nil {
			continue
		}
		slice, tokens := idx.fileSlice(fe, 80, remaining)
		if tokens <= 0 {
			continue
		}
		d.ImplicatedFiles = append(d.ImplicatedFiles, slice)
		d.EstimatedTokens += tokens
		remaining -= tokens
		if remaining <= 0 {
			break
		}
	}
	return d, nil
}

func (d *DeltaRepairContext) coreBlock() string {
	var b strings.Builder
	b.WriteString("## Finding\n")
	b.WriteString(d.Severity)
	b.WriteString(": ")
	b.WriteString(d.Finding)
	b.WriteString("\n\n## Next objective\n")
	b.WriteString(d.NextObjective)
	if d.Diff != "" {
		b.WriteString("\n\n## Current diff\n```\n")
		b.WriteString(d.Diff)
		b.WriteString("\n```\n")
	}
	if d.FailingTest != "" {
		b.WriteString("\n## Failing test\n")
		b.WriteString(d.FailingTest)
		b.WriteString("\n")
	}
	if d.FailingTestLog != "" {
		b.WriteString("\n## Test log (sliced)\n")
		b.WriteString(d.FailingTestLog)
	}
	return b.String()
}

// Render produces the agent-readable delta repair text.
func (d *DeltaRepairContext) Render() string {
	out := d.coreBlock()
	for _, sl := range d.ImplicatedFiles {
		out += "\n## File: " + sl.Path + "\n```\n" + sl.Excerpt + "\n```\n"
	}
	return out
}
