package repoinfo

import (
	"fmt"
	"sort"
	"strings"
)

// ContextPack is the compact payload a coding agent receives (spec §22.3). It is
// deliberately NOT the whole repository — only specification, allowed scope, a
// repo map, the most relevant files, architectural rules, commands, recent
// failures and links to full artifacts.
//
// Assembly respects a token budget (§22.1): the pack is trimmed until its
// estimated token cost fits within Budget. When trimming, the least-relevant
// files are dropped first (never the specification or scope).
type ContextPack struct {
	Specification       string
	AllowedScope        []string
	RepoMap             string
	RelevantFiles       []FileSlice
	ArchitecturalRules  []string
	Commands            []string
	RecentFailures      []string
	ArtifactLinks       []ArtifactLink
	EstimatedTokens     int
	Budget              int
	TrimmedFilesDropped int
}

// FileSlice is a trimmed excerpt of a file included in the pack.
type FileSlice struct {
	Path       string
	Language   string
	Excerpt    string
	LinesFrom  int
	LinesTo    int
	FullTokens int // estimated tokens of the FULL file (for the artifact link)
}

// ArtifactLink points at a full artifact the agent may retrieve on demand
// instead of having it dumped into the prompt (§22.3 "links to full artifacts").
type ArtifactLink struct {
	Kind string // "file" | "log" | "attachment"
	Path string
	Note string
}

// PackOptions controls Context Pack assembly.
type PackOptions struct {
	// Specification text (already compiled by the task compiler).
	Specification string
	// AllowedScope is the list of paths the agent is permitted to touch.
	AllowedScope []string
	// QueryTerms drive relevance ranking (typically derived from the task).
	QueryTerms []string
	// ArchitecturalRules (from the project constitution).
	ArchitecturalRules []string
	// Commands (build/test/lint) the agent should use.
	Commands []string
	// RecentFailures to surface (already sliced, §22.4).
	RecentFailures []string
	// Budget is the maximum estimated tokens for the pack (§22.1).
	Budget int
	// MaxFiles caps how many file slices may be included.
	MaxFiles int
	// ExcerptLines is the max lines per file slice.
	ExcerptLines int
}

// DefaultBudget is a sane default token budget for a context pack (§22.1).
const DefaultBudget = 24000

// AssemblePack builds a Context Pack from the index + options, enforcing the
// token budget (§22.1). It never includes the whole repository.
func (idx *Index) AssemblePack(opts PackOptions) (*ContextPack, error) {
	if opts.Budget <= 0 {
		opts.Budget = DefaultBudget
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 20
	}
	if opts.ExcerptLines <= 0 {
		opts.ExcerptLines = 120
	}

	pack := &ContextPack{
		Specification:      opts.Specification,
		AllowedScope:       cloneStrings(opts.AllowedScope),
		ArchitecturalRules: cloneStrings(opts.ArchitecturalRules),
		Commands:           cloneStrings(opts.Commands),
		RecentFailures:     cloneStrings(opts.RecentFailures),
		Budget:             opts.Budget,
	}

	// The specification + scope + rules + commands are non-trimmable core.
	core := pack.coreBlock()
	pack.EstimatedTokens += EstimateTokens(core)
	if pack.EstimatedTokens > opts.Budget {
		// Even the core does not fit. We still return a pack containing only
		// the core (the agent needs at least the spec); the caller sees the
		// overflow in EstimatedTokens and can act.
		return pack, nil
	}

	// Repo map (compact, path-only). Trimmed last; it is cheap.
	repoMap := idx.repoMapText()
	mapTokens := EstimateTokens(repoMap)
	if pack.EstimatedTokens+mapTokens <= opts.Budget {
		pack.RepoMap = repoMap
		pack.EstimatedTokens += mapTokens
	}

	// Relevant files: rank by query terms + allowed scope + related changes.
	candidates := idx.rankRelevant(opts)
	remaining := opts.Budget - pack.EstimatedTokens
	included := 0
	for _, c := range candidates {
		if included >= opts.MaxFiles {
			break
		}
		slice, tokens := idx.fileSlice(c, opts.ExcerptLines, remaining)
		if tokens <= 0 {
			pack.TrimmedFilesDropped++
			continue
		}
		pack.RelevantFiles = append(pack.RelevantFiles, slice)
		pack.EstimatedTokens += tokens
		remaining -= tokens
		included++
		// Append a link to the full file.
		pack.ArtifactLinks = append(pack.ArtifactLinks, ArtifactLink{
			Kind: "file", Path: slice.Path,
			Note: fmt.Sprintf("full file (~%d tokens)", slice.FullTokens),
		})
		if remaining <= EstimateTokens("\n") {
			break
		}
	}
	pack.TrimmedFilesDropped += len(candidates) - included
	return pack, nil
}

func (p *ContextPack) coreBlock() string {
	var b strings.Builder
	b.WriteString("## Specification\n")
	b.WriteString(p.Specification)
	b.WriteString("\n\n## Allowed scope\n")
	for _, s := range p.AllowedScope {
		b.WriteString("- " + s + "\n")
	}
	if len(p.ArchitecturalRules) > 0 {
		b.WriteString("\n## Architectural rules\n")
		for _, r := range p.ArchitecturalRules {
			b.WriteString("- " + r + "\n")
		}
	}
	if len(p.Commands) > 0 {
		b.WriteString("\n## Commands\n")
		for _, c := range p.Commands {
			b.WriteString("- " + c + "\n")
		}
	}
	if len(p.RecentFailures) > 0 {
		b.WriteString("\n## Recent failures\n")
		for _, f := range p.RecentFailures {
			b.WriteString("- " + f + "\n")
		}
	}
	return b.String()
}

func (idx *Index) repoMapText() string {
	var b strings.Builder
	for i := range idx.Files {
		fe := &idx.Files[i]
		b.WriteString(fe.Path)
		if fe.Language != "" {
			b.WriteString("  [" + fe.Language + "]")
		}
		if fe.IsTest {
			b.WriteString(" (test)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (idx *Index) rankRelevant(opts PackOptions) []*FileEntry {
	type scored struct {
		fe    *FileEntry
		score int
	}
	var ranked []scored
	// Score by query terms.
	matches := idx.Search(strings.Join(opts.QueryTerms, " "), 0)
	scoreByPath := map[string]int{}
	for _, m := range matches {
		scoreByPath[m.Path] = m.Score
	}
	// Boost allowed-scope files and related changes.
	for i := range idx.Files {
		fe := &idx.Files[i]
		score := scoreByPath[fe.Path]
		for _, s := range opts.AllowedScope {
			if fe.Path == s || strings.HasPrefix(fe.Path, s+"/") {
				score += 10
			}
		}
		if score > 0 {
			ranked = append(ranked, scored{fe, score})
		}
	}
	// Add related changes for the explicit scope.
	for _, rel := range idx.RelatedChanges(opts.AllowedScope, 0) {
		if fe := idx.ByPath[rel.Path]; fe != nil && scoreByPath[fe.Path] == 0 {
			ranked = append(ranked, scored{fe, rel.Score})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].fe.Path < ranked[j].fe.Path
	})
	out := make([]*FileEntry, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.fe)
	}
	return out
}

func (idx *Index) fileSlice(fe *FileEntry, maxLines, tokenBudget int) (FileSlice, int) {
	full := ""
	if f := openRead(idx.Root, fe.Path); f != nil {
		full = readText(f, maxLines)
	}
	if full == "" {
		return FileSlice{Path: fe.Path}, 0
	}
	// Trim to the budget if necessary.
	tokens := EstimateTokens(full)
	for tokens > tokenBudget && maxLines > 10 {
		maxLines = maxLines / 2
		full = readText(mustOpen(idx.Root, fe.Path), maxLines)
		tokens = EstimateTokens(full)
	}
	if tokens > tokenBudget {
		return FileSlice{Path: fe.Path, FullTokens: fe.estimateFullTokens()}, 0
	}
	return FileSlice{
		Path:       fe.Path,
		Language:   fe.Language,
		Excerpt:    full,
		LinesFrom:  1,
		LinesTo:    min(maxLines, fe.Lines),
		FullTokens: fe.estimateFullTokens(),
	}, tokens
}

func (fe *FileEntry) estimateFullTokens() int {
	// ~4 chars per token is a stable, conservative estimate.
	if fe.Size > 0 {
		return int(fe.Size) / 4
	}
	return fe.Lines * 10
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
