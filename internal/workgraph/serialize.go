// This file implements stable serialization for the work-graph domain model
// (spec §18.3). MarshalWorkGraph produces canonical JSON: packages sorted by
// ID, and within each package every list (AcceptedACIDs, AllowedScope,
// Dependencies) sorted lexicographically, so two structurally-equal graphs
// always marshal to byte-identical output. UnmarshalWorkGraph round-trips a
// marshalled graph losslessly (the canonical form is the identity form).
//
// The serialised payload is the transport/persistence shape; it deliberately
// mirrors the JSON tags on WorkGraph / WorkPackage / Attempt so callers can also
// use encoding/json directly when they do not need canonicalisation guarantees.

package workgraph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// serializedGraph is the on-the-wire shape. It matches WorkGraph's JSON tags but
// is declared explicitly so future internal refactors do not silently change the
// serialised contract.
type serializedGraph struct {
	TaskID   string          `json:"task_id"`
	Packages []serializedPkg `json:"packages"`
}

type serializedPkg struct {
	ID            string       `json:"id"`
	TaskID        string       `json:"task_id"`
	Stage         Stage        `json:"stage"`
	Title         string       `json:"title"`
	Objective     string       `json:"objective"`
	AcceptedACIDs []string     `json:"accepted_ac_ids"`
	AllowedScope  []string     `json:"allowed_scope,omitempty"`
	Dependencies  []string     `json:"dependencies,omitempty"`
	State         PackageState `json:"state"`
	Attempts      []Attempt    `json:"attempts,omitempty"`
}

// Canonicalize returns a WorkGraph whose packages are sorted by ID and whose
// per-package string lists are sorted lexicographically and de-duplicated. The
// result is the canonical input for MarshalWorkGraph. Canonicalize is a pure
// function; it does not validate the graph.
func Canonicalize(g WorkGraph) WorkGraph {
	out := WorkGraph{TaskID: g.TaskID, Packages: make([]WorkPackage, len(g.Packages))}
	for i, p := range g.Packages {
		pk := p.clone()
		pk.AcceptedACIDs = sortedStrings(p.AcceptedACIDs)
		pk.AllowedScope = sortedStrings(p.AllowedScope)
		pk.Dependencies = sortedStrings(p.Dependencies)
		out.Packages[i] = pk
	}
	sort.SliceStable(out.Packages, func(i, j int) bool {
		return out.Packages[i].ID < out.Packages[j].ID
	})
	return out
}

// MarshalWorkGraph encodes g as canonical JSON. The output is byte-identical for
// any two structurally-equal graphs (same TaskID, same set of packages with the
// same per-package fields), regardless of input ordering. It does not validate
// the graph; callers that need a runnable handle must first obtain a
// ValidatedWorkGraph.
func MarshalWorkGraph(g WorkGraph) ([]byte, error) {
	canon := Canonicalize(g)
	sg := serializedGraph{TaskID: canon.TaskID, Packages: make([]serializedPkg, len(canon.Packages))}
	for i, p := range canon.Packages {
		sg.Packages[i] = serializedPkg{
			ID:            p.ID,
			TaskID:        p.TaskID,
			Stage:         p.Stage,
			Title:         p.Title,
			Objective:     p.Objective,
			AcceptedACIDs: nonNil(p.AcceptedACIDs),
			AllowedScope:  p.AllowedScope, // omitempty keeps nil
			Dependencies:  p.Dependencies, // omitempty keeps nil
			State:         p.State,
			Attempts:      p.Attempts, // omitempty keeps nil
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&sg); err != nil {
		return nil, fmt.Errorf("workgraph: marshal: %w", err)
	}
	// json.Encoder appends a trailing newline; trim it so the output is a pure
	// canonical token (two structurally-equal graphs must produce identical
	// bytes, and callers may compose the bytes inside larger JSON documents).
	out := bytes.TrimRight(buf.Bytes(), "\n")
	return out, nil
}

// UnmarshalWorkGraph decodes JSON produced by MarshalWorkGraph (or any
// structurally-compatible source) into a WorkGraph. It is the inverse of
// MarshalWorkGraph: UnmarshalWorkGraph(MarshalWorkGraph(g)) is structurally
// equal to Canonicalize(g). The returned graph is NOT validated; call
// ValidateWorkGraph to obtain a runnable handle.
func UnmarshalWorkGraph(data []byte) (WorkGraph, error) {
	var sg serializedGraph
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sg); err != nil {
		return WorkGraph{}, fmt.Errorf("workgraph: unmarshal: %w", err)
	}
	g := WorkGraph{TaskID: sg.TaskID, Packages: make([]WorkPackage, len(sg.Packages))}
	for i, sp := range sg.Packages {
		g.Packages[i] = WorkPackage{
			ID:            sp.ID,
			TaskID:        sp.TaskID,
			Stage:         sp.Stage,
			Title:         sp.Title,
			Objective:     sp.Objective,
			AcceptedACIDs: nonNil(sp.AcceptedACIDs),
			AllowedScope:  sp.AllowedScope,
			Dependencies:  sp.Dependencies,
			State:         sp.State,
			Attempts:      sp.Attempts,
		}
	}
	return g, nil
}

// MarshalValidated serialises a ValidatedWorkGraph. Because the validated graph
// is already canonical (ValidateWorkGraph sorts packages by ID and the validated
// packages' lists are the input lists), the output matches MarshalWorkGraph of
// the underlying graph.
func MarshalValidated(v *ValidatedWorkGraph) ([]byte, error) {
	if v == nil {
		return nil, fmt.Errorf("workgraph: marshal: nil validated graph")
	}
	return MarshalWorkGraph(v.graph)
}

// sortedStrings returns a sorted, de-duplicated copy of in (nil/empty → nil).
func sortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// nonNil guarantees a non-nil slice when the input has elements, so the JSON
// shape for "one AC" is `["AC-1"]` rather than `null`. Empty/nil input stays
// nil so omitempty behaves correctly elsewhere.
func nonNil(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	return out
}
