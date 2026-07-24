package opencode

import (
	"strings"
	"testing"
)

func TestRedactSecretsKeyValues(t *testing.T) {
	cases := []struct{ in string }{
		{`error: api_key=sk-1234567890abcdef hit limit`},
		{`Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9abcdef`},
		{`token=ghp_0123456789abcdefXYZ`},
		{`password=supersecretpassword123`},
	}
	for _, c := range cases {
		out := redactSecrets(c.in)
		if out == c.in {
			t.Errorf("secret not redacted in %q (got %q)", c.in, out)
		}
	}
}

func TestRedactPreservesSignalText(t *testing.T) {
	in := "HTTP 401 unauthorized: invalid api key=sk-1234567890abcdef"
	out := redactSecrets(in)
	// The classification signal words must survive redaction.
	for _, frag := range []string{"401", "unauthorized", "invalid api key"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(frag)) {
			t.Errorf("signal fragment %q lost after redaction: %q", frag, out)
		}
	}
	if strings.Contains(out, "sk-1234567890abcdef") {
		t.Errorf("secret value survived: %q", out)
	}
}

func TestRedactEmpty(t *testing.T) {
	if redactSecrets("") != "" {
		t.Error("empty should stay empty")
	}
}

func TestRedactNoFalsePositivesOnShortText(t *testing.T) {
	in := "quota exhausted before any edits"
	if redactSecrets(in) != in {
		t.Errorf("short plain text should be untouched: %q -> %q", in, redactSecrets(in))
	}
}

func TestRedactBytes(t *testing.T) {
	out := redactBytes([]byte("token=abcdefghijkmnop12345"))
	if strings.Contains(string(out), "abcdefghijkmnop12345") {
		t.Errorf("redactBytes leaked: %q", out)
	}
}
