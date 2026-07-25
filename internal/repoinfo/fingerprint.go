package repoinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Prompt-cache fingerprinting (spec §22.8). Providers that support prompt
// caching can reuse context only when the prefix is byte-stable. We therefore
// give the stable parts of the Context Pack (project instructions, repo map,
// architectural rules, commands) a deterministic ORDER and a fingerprint so the
// daemon can detect when a cached prefix is still valid.

// Fingerprint is the stable hash of a prompt prefix.
type Fingerprint struct {
	Hash       string
	PartCount  int
	ByteLength int
}

// FingerprintPrompt returns a stable fingerprint for the stable prefix parts of
// a prompt. Parts are sorted (deterministic order, §22.8) before hashing, so
// the same logical content always yields the same fingerprint regardless of map
// iteration order.
func FingerprintPrompt(parts []string) Fingerprint {
	sorted := sortedUnique(parts)
	var b strings.Builder
	for _, p := range sorted {
		b.WriteString(p)
		b.WriteByte('\n')
		b.WriteString(separator)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return Fingerprint{
		Hash:       hex.EncodeToString(sum[:]),
		PartCount:  len(sorted),
		ByteLength: b.Len(),
	}
}

const separator = "---neuroforge-part---"

// StablePrefix renders the stable parts of a Context Pack in a deterministic
// order so prompt-cache prefixes are byte-stable (§22.8). Volatile parts
// (per-task spec, recent failures, sliced logs) are NOT included — they belong
// after the cacheable prefix.
func StablePrefix(rules, commands []string, repoMap string) string {
	var b strings.Builder
	rules = sortedUnique(rules)
	for _, r := range rules {
		b.WriteString("RULE: ")
		b.WriteString(r)
		b.WriteByte('\n')
	}
	commands = sortedUnique(commands)
	for _, c := range commands {
		b.WriteString("CMD: ")
		b.WriteString(c)
		b.WriteByte('\n')
	}
	if repoMap != "" {
		b.WriteString("REPO_MAP_BEGIN\n")
		b.WriteString(repoMap)
		b.WriteString("REPO_MAP_END\n")
	}
	return b.String()
}

// IsCacheHit reports whether the new fingerprint matches the cached one. The
// daemon keeps the last fingerprint per project+provider and reuses the cached
// prefix when this returns true.
func IsCacheHit(cached, new Fingerprint) bool {
	return cached.Hash != "" && cached.Hash == new.Hash
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
