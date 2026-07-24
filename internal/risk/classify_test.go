package risk

import (
	"testing"
)

func TestClassify_TableDriven(t *testing.T) {
	cases := []struct {
		name   string
		signs  Signals
		want   Level
		reason string // substring expected in reasons
	}{
		{
			name:   "plain docs change",
			signs:  Signals{Description: "fix typo in README"},
			want:   R0,
			reason: "documentation/mechanical",
		},
		{
			name:   "analytics dashboard keyword",
			signs:  Signals{Description: "add analytics dashboard widget"},
			want:   R1,
			reason: "keyword hint",
		},
		{
			name:   "local UI component via path",
			signs:  Signals{Paths: []string{"src/ui/components/Button.tsx"}},
			want:   R1,
			reason: "path hint",
		},
		{
			name:   "public API change",
			signs:  Signals{PublicAPIChange: true, Description: "extend handler"},
			want:   R2,
			reason: "changes public API",
		},
		{
			name:   "webhook integration keyword",
			signs:  Signals{Description: "add webhook integration for provider"},
			want:   R2,
			reason: "keyword hint",
		},
		{
			name:   "db migration",
			signs:  Signals{HasMigrations: true},
			want:   R3,
			reason: "database migrations",
		},
		{
			name:   "subscription contract change",
			signs:  Signals{SubscriptionChange: true},
			want:   R3,
			reason: "subscription contracts",
		},
		{
			name:   "auth touch structural",
			signs:  Signals{TouchesAuth: true, Description: "refactor helper"},
			want:   R4,
			reason: "authentication",
		},
		{
			name:   "payments path dominates lower keyword",
			signs:  Signals{Paths: []string{"billing/charge.go"}, Description: "small refactor"},
			want:   R4,
			reason: "path hint",
		},
		{
			name:   "destructive command",
			signs:  Signals{DestructiveCommands: true, Description: "cleanup script"},
			want:   R4,
			reason: "destructive",
		},
		{
			name: "highest wins among many",
			signs: Signals{
				Description:       "add analytics report",
				PublicAPIChange:   true,
				ConcurrencyChange: true,
				TouchesAuth:       true,
			},
			want:   R4,
			reason: "authentication",
		},
		{
			name:   "permissions flag",
			signs:  Signals{TouchesPermissions: true},
			want:   R4,
			reason: "permissions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.signs)
			if got.Level != tc.want {
				t.Fatalf("level = %s, want %s; reasons=%v", got.Level, tc.want, got.Reasons)
			}
			found := false
			for _, r := range got.Reasons {
				if contains(r, tc.reason) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected a reason containing %q, got %v", tc.reason, got.Reasons)
			}
		})
	}
}

func TestLevelOrderingAndStrings(t *testing.T) {
	want := []string{"R0", "R1", "R2", "R3", "R4"}
	got := Levels()
	if len(got) != len(want) {
		t.Fatalf("Levels() length = %d, want %d", len(got), len(want))
	}
	for i, l := range got {
		if l.String() != want[i] {
			t.Errorf("Levels()[%d].String() = %q, want %q", i, l.String(), want[i])
		}
		if !l.IsValid() {
			t.Errorf("level %s not valid", l)
		}
	}
	if !(R4).AtLeast(R3) {
		t.Error("R4.AtLeast(R3) = false, want true")
	}
	if R2.AtLeast(R3) {
		t.Error("R2.AtLeast(R3) = true, want false")
	}
	if !MaxLevel.IsValid() {
		t.Error("MaxLevel invalid")
	}
}

func TestReasonsAlwaysNonEmpty(t *testing.T) {
	r := Classify(Signals{})
	if len(r.Reasons) == 0 {
		t.Error("empty signals must still produce a non-empty reason list")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
