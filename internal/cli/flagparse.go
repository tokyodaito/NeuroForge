package cli

import (
	"flag"
	"strings"
)

// reorderFlagsAndArgs reorders args so that all flag tokens precede the
// positional (non-flag) tokens, without changing flag→value associations. This
// lets the stdlib flag package — which stops parsing at the first positional
// argument — correctly honour interspersed forms like:
//
//	<id> --engine opencode --json
//
// as well as the canonical:
//
//	--engine opencode --json <id>
//
// It works by walking args once and classifying each token:
//   - "--" terminates flag scanning; the rest are positional.
//   - a token starting with "-" or "--" (and longer than "-") is a flag.
//     Whether it consumes the NEXT token as its value is decided by looking the
//     flag name up in fs: boolean flags (IsBoolFlag()==true) take no value;
//     every other registered flag consumes the next token. "--name=value" and
//     "-name=value" never consume a following token.
//   - any other token is positional.
//
// Unknown flags (not registered in fs) are passed through unchanged so that
// fs.Parse — not this helper — reports the "flag provided but not defined"
// error. This keeps the helper from silently swallowing unknown flags.
//
// The positional tokens are appended after the flag tokens, preserving their
// relative order.
func reorderFlagsAndArgs(fs *flag.FlagSet, args []string) []string {
	var flags, positionals []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			if idx := strings.IndexByte(a, '='); idx < 0 {
				// no "=": decide if the next token is this flag's value.
				name := strings.TrimLeft(a, "-")
				if !consumesNextValue(fs, name) {
					// boolean/unknown flag: do not swallow next token.
				} else if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
		} else {
			positionals = append(positionals, a)
		}
		i++
	}
	return append(flags, positionals...)
}

// consumesNextValue reports whether the registered flag named name takes a
// following token as its value (i.e. it is NOT a boolean flag). Unknown flags
// report false so they are never silently paired with a following token.
func consumesNextValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
		return !bf.IsBoolFlag()
	}
	return true
}

// parseWithPositionalReorder parses args against fs after reordering so flags
// may appear before OR after the positional argument(s). It returns the
// positional remainder (fs.Args()) on success. fs must use ContinueOnError.
func parseWithPositionalReorder(fs *flag.FlagSet, args []string) ([]string, error) {
	reordered := reorderFlagsAndArgs(fs, args)
	if err := fs.Parse(reordered); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}
