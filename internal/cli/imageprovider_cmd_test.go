package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestImageProvider_CLI(t *testing.T) {
	t.Setenv("NEUROFORGE_HOME", t.TempDir())
	cases := []struct {
		args     []string
		wantOut  string
		wantCode int
	}{
		{[]string{"image-provider", "list", "--json"}, `"id"`, 0},
		{[]string{"image-provider", "list"}, "IMAGE PROVIDERS", 0},
		{[]string{"image-provider", "doctor"}, "image-provider conformance", 0},
		{[]string{"image-provider", "doctor", "--json"}, `"name"`, 0},
		{[]string{"image-provider"}, "", 1},
	}
	for _, c := range cases {
		var out, errs bytes.Buffer
		app := &App{Name: "forge", Out: &out, Err: &errs, Stdin: bytes.NewReader(nil)}
		code := app.Run(c.args)
		if code != c.wantCode {
			t.Errorf("%v: code = %d, want %d (stderr=%q)", c.args, code, c.wantCode, errs.String())
		}
		if c.wantCode == 0 && !strings.Contains(out.String(), c.wantOut) {
			t.Errorf("%v: stdout missing %q\n%s", c.args, c.wantOut, out.String())
		}
	}
}
