package policy

import "testing"

func TestInjectionOrder_IsCanonicalSpecSequence(t *testing.T) {
	t.Parallel()
	want := []InjectionLayer{
		LayerFactoryPolicy,
		LayerConstitution,
		LayerTaskSpec,
		LayerRepoDocs,
		LayerSourceComments,
		LayerExternalAttachments,
	}
	if len(InjectionOrder) != len(want) {
		t.Fatalf("order length = %d, want %d", len(InjectionOrder), len(want))
	}
	for i, l := range InjectionOrder {
		if l != want[i] {
			t.Errorf("InjectionOrder[%d] = %v, want %v", i, l, want[i])
		}
	}
}

func TestHigherPriority(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b InjectionLayer
		want bool
	}{
		{LayerFactoryPolicy, LayerRepoDocs, true},              // factory > README
		{LayerConstitution, LayerTaskSpec, true},               // constitution > task spec
		{LayerExternalAttachments, LayerSourceComments, false}, // attachments < comments
		{LayerTaskSpec, LayerTaskSpec, false},                  // equal
	}
	for _, c := range cases {
		if got := HigherPriority(c.a, c.b); got != c.want {
			t.Errorf("HigherPriority(%v,%v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestComparePriority(t *testing.T) {
	t.Parallel()
	if ComparePriority(LayerFactoryPolicy, LayerRepoDocs) != -1 {
		t.Error("factory should compare higher (-1) than repo docs")
	}
	if ComparePriority(LayerRepoDocs, LayerFactoryPolicy) != 1 {
		t.Error("repo docs should compare lower (+1) than factory")
	}
	if ComparePriority(LayerTaskSpec, LayerTaskSpec) != 0 {
		t.Error("equal layers should compare 0")
	}
}

// TestInjectionPriority_AnInstructionInDocsCannotEnablePush: the spec §29.3
// invariant made executable — an instruction in a low layer never overrides a
// higher layer.
func TestInjectionPriority_AnInstructionInDocsCannotEnablePush(t *testing.T) {
	t.Parallel()
	// A LOCAL_REVIEW (network-locked) project: push is off at the Factory Policy
	// layer. An instruction arriving in repo docs cannot override that.
	res, _ := Resolve(NewProject(ProfileLocalReview), TaskContext{})
	if res.Allows(ActPush).Allow {
		t.Fatal("push must remain denied; a README instruction cannot enable it")
	}
	if !Wins(LayerFactoryPolicy, LayerRepoDocs) {
		t.Fatal("Factory Policy must win over Repository Documentation")
	}
	if Wins(LayerRepoDocs, LayerFactoryPolicy) {
		t.Fatal("repo docs must never win over Factory Policy")
	}
}

func TestInjectionLayer_String(t *testing.T) {
	t.Parallel()
	for _, l := range InjectionOrder {
		if l.String() == "" {
			t.Errorf("layer %d has empty string", int(l))
		}
	}
}
