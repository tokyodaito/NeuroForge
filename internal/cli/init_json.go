package cli

import (
	"fmt"
	"strings"

	"neuroforge/internal/bootstrap"
)

// planJSON renders an installation plan as best-effort JSON without pulling a
// dependency into the bootstrap core.
func planJSON(p *bootstrap.InstallPlan) string {
	if p == nil {
		return "{}"
	}
	var b strings.Builder
	b.WriteString(`{"profile":`)
	b.WriteString(fmt.Sprintf("%q", string(p.Profile)))
	b.WriteString(`,"requires_sudo":`)
	b.WriteString(fmt.Sprintf("%v", p.RequiresSudo))
	b.WriteString(`,"requires_shell_change":`)
	b.WriteString(fmt.Sprintf("%v", p.RequiresShellProfileChange))
	b.WriteString(`,"will_install":[`)
	for i, s := range p.WillInstall {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf(`{"tool":%q,"action":%q,"needs_sudo":%v}`, s.ToolID, s.Action, s.NeedsSudo))
	}
	b.WriteString(`],"wont_install":[`)
	for i, s := range p.WontInstall {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf(`{"tool":%q,"reason":%q}`, s.ToolID, s.Reason))
	}
	b.WriteString(`]}`)
	return b.String()
}
