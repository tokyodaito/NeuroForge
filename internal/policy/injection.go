package policy

import "fmt"

// InjectionLayer is a layer of the prompt-injection priority order (spec §29.3).
// Instructions in a lower-priority layer can never override a higher-priority
// one — e.g. an instruction inside a README cannot enable push or disable
// security.
type InjectionLayer int

const (
	// LayerFactoryPolicy is the highest priority, immutable Factory Security
	// Policy. Never overridable.
	LayerFactoryPolicy InjectionLayer = iota
	// LayerConstitution is the project constitution.
	LayerConstitution
	// LayerTaskSpec is the compiled task specification.
	LayerTaskSpec
	// LayerRepoDocs is repository documentation (README, docs).
	LayerRepoDocs
	// LayerSourceComments are source-code comments.
	LayerSourceComments
	// LayerExternalAttachments is the lowest priority: attachments and other
	// untrusted content supplied with a task.
	LayerExternalAttachments
)

// String returns the spec layer name.
func (l InjectionLayer) String() string {
	switch l {
	case LayerFactoryPolicy:
		return "Factory Security Policy"
	case LayerConstitution:
		return "Project Constitution"
	case LayerTaskSpec:
		return "Task Specification"
	case LayerRepoDocs:
		return "Repository Documentation"
	case LayerSourceComments:
		return "Source Comments"
	case LayerExternalAttachments:
		return "External Attachments"
	default:
		return fmt.Sprintf("InjectionLayer(%d)", int(l))
	}
}

// InjectionOrder is the fixed §29.3 priority sequence, highest first.
var InjectionOrder = []InjectionLayer{
	LayerFactoryPolicy,
	LayerConstitution,
	LayerTaskSpec,
	LayerRepoDocs,
	LayerSourceComments,
	LayerExternalAttachments,
}

// HigherPriority reports whether a has strictly higher prompt-injection priority
// than b (i.e. a wins when they conflict).
func HigherPriority(a, b InjectionLayer) bool { return a < b }

// ComparePriority returns -1, 0, +1 comparing the priority of a and b.
func ComparePriority(a, b InjectionLayer) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Wins reports whether an instruction in the higher layer overrides one in the
// lower layer. An instruction in `lower` (e.g. External Attachments) can never
// override `higher` (e.g. Factory Policy).
func Wins(higher, lower InjectionLayer) bool {
	return higher < lower
}
