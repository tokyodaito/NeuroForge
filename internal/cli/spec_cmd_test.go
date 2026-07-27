package cli

import (
	"testing"

	"neuroforge/internal/task"
)

// TestSplitAttachFlag_Grammar is the MAJOR-1 unit-level regression test for
// the extended --attach grammar. The production CLI path depends on this
// parser to supply the metadata (filename/MIME/size) the compiler is
// documented to consume (spec §9.5). A regression here would silently revert
// the CLI to hash+role-only and reintroduce the degenerate "()" objective.
func TestSplitAttachFlag_Grammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want task.Attachment
		ok   bool
	}{
		{
			name: "legacy hash=ROLE",
			in:   "sha256:abc=REQUIREMENTS",
			want: task.Attachment{Hash: "sha256:abc", Role: task.RoleRequirements},
			ok:   true,
		},
		{
			name: "hash=ROLE:filename",
			in:   "sha256:abc=REQUIREMENTS:requirements.md",
			want: task.Attachment{
				Hash:     "sha256:abc",
				Role:     task.RoleRequirements,
				Filename: "requirements.md",
			},
			ok: true,
		},
		{
			name: "hash=ROLE:filename:mimeType",
			in:   "sha256:abc=REQUIREMENTS:requirements.md:text/markdown",
			want: task.Attachment{
				Hash:     "sha256:abc",
				Role:     task.RoleRequirements,
				Filename: "requirements.md",
				MimeType: "text/markdown",
			},
			ok: true,
		},
		{
			name: "hash=ROLE:filename:mimeType:size",
			in:   "sha256:abc=REQUIREMENTS:requirements.md:text/markdown:512",
			want: task.Attachment{
				Hash:     "sha256:abc",
				Role:     task.RoleRequirements,
				Filename: "requirements.md",
				MimeType: "text/markdown",
				Size:     512,
			},
			ok: true,
		},
		{
			name: "role lowercased is normalised to upper",
			in:   "sha256:abc=design_reference",
			want: task.Attachment{Hash: "sha256:abc", Role: task.RoleDesignReference},
			ok:   true,
		},
		{
			name: "whitespace around role is trimmed",
			in:   "sha256:abc=  REQUIREMENTS  ",
			want: task.Attachment{Hash: "sha256:abc", Role: task.RoleRequirements},
			ok:   true,
		},
		{
			name: "trailing empty size is tolerated (hash=ROLE:f:mt:)",
			in:   "sha256:abc=REQUIREMENTS:req.md:text/markdown:",
			want: task.Attachment{
				Hash:     "sha256:abc",
				Role:     task.RoleRequirements,
				Filename: "req.md",
				MimeType: "text/markdown",
			},
			ok: true,
		},
		{
			name: "unknown role rejected",
			in:   "sha256:abc=NOT_A_ROLE",
			ok:   false,
		},
		{
			name: "missing hash rejected",
			in:   "=REQUIREMENTS",
			ok:   false,
		},
		{
			name: "missing role rejected",
			in:   "sha256:abc=",
			ok:   false,
		},
		{
			name: "no equals sign rejected",
			in:   "sha256:abc",
			ok:   false,
		},
		{
			name: "non-numeric size rejected",
			in:   "sha256:abc=REQUIREMENTS:req.md:text/markdown:not-a-number",
			ok:   false,
		},
		{
			name: "negative size rejected",
			in:   "sha256:abc=REQUIREMENTS:req.md:text/markdown:-5",
			ok:   false,
		},
		{
			name: "empty string rejected",
			in:   "",
			ok:   false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := splitAttachFlag(tc.in)
			if ok != tc.ok {
				t.Fatalf("splitAttachFlag(%q) ok = %v, want %v", tc.in, ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got != tc.want {
				t.Fatalf("splitAttachFlag(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
