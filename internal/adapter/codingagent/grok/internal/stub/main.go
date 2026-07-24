// Command grokstub is a tiny, self-contained test fixture that emulates the
// headless `grok --output-format streaming-json` wire format consumed by the
// Grok adapter. It is NOT the real Grok CLI and makes no network or paid API
// calls (rule §36.5). It is driven entirely by the GROK_STUB_SCENARIO env var
// so adapter integration tests (unit + conformance) can exercise every code
// path deterministically: success, malformed output, hang/cancel, timeout,
// quota/rate-limit/auth/capacity failures, crash, resume, scope violation and
// usage events.
//
// `--version` prints a parseable version string for adapter detection.
//
// Build it from the adapter package test helpers; never register or ship it.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const versionLine = "grok version 0.9.0\n"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "--version" {
		fmt.Print(versionLine)
		return
	}
	scenario := strings.ToLower(strings.TrimSpace(os.Getenv("GROK_STUB_SCENARIO")))
	run(scenario, os.Stdout, os.Stderr)
}

// run executes one scenario against the given writers and sets the exit code.
func run(scenario string, stdout io.Writer, stderr io.Writer) {
	switch scenario {
	case "", "success":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"message","role":"assistant","delta":"Hello from grok stub"}`)
		emit(stdout, `{"type":"file","path":"src/hello.txt","action":"created"}`)
		emit(stdout, `{"type":"usage","input_tokens":120,"output_tokens":80,"cost":0.0001,"currency":"USD"}`)
		emit(stdout, `{"type":"result","status":"completed","text":"done"}`)
		os.Exit(0)

	case "malformed-json":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"message","role":"assistant","delta":"working"}`)
		emitRaw(stdout, `{not valid json`) // malformed line: must not break the run
		emit(stdout, `{"type":"result","status":"completed","text":"done despite malformed"}`)
		os.Exit(0)

	case "partial-output":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"message","role":"assistant","delta":"partial..."}`)
		// Exit mid-stream with no terminal item.
		os.Exit(1)

	case "crash":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		fmt.Fprint(stderr, "grok stub panicked (simulated crash)\n")
		os.Exit(134)

	case "quota-before-edits":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"usage","input_tokens":0,"output_tokens":0}`)
		fmt.Fprint(stderr, "error: quota exhausted before any edits\n")
		os.Exit(2)

	case "quota-after-edits":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"file","path":"src/edit.txt","action":"modified"}`)
		emit(stdout, `{"type":"usage","input_tokens":200,"output_tokens":100,"cost":0.0002}`)
		fmt.Fprint(stderr, "error: quota exhausted after edits\n")
		os.Exit(2)

	case "rate-limit":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"error","code":"rate_limit","message":"HTTP 429 too many requests"}`)
		fmt.Fprint(stderr, "HTTP 429 too many requests\n")
		os.Exit(2)

	case "auth-failure":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		fmt.Fprint(stderr, "401 unauthorized: invalid api key sk-abcd1234\n")
		os.Exit(2)

	case "capacity":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"error","code":"capacity","message":"provider overloaded"}`)
		fmt.Fprint(stderr, "provider capacity: overloaded (503)\n")
		os.Exit(2)

	case "model-not-available":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		fmt.Fprint(stderr, "model not available for this account\n")
		os.Exit(2)

	case "scope-violation":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"file","path":"OUTSIDE_SCOPE/secret.txt","action":"created"}`)
		fmt.Fprint(stderr, "scope violation: wrote outside allowed paths\n")
		os.Exit(2)

	case "usage-events":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"usage","input_tokens":100,"output_tokens":50,"cost":0.0001}`)
		emit(stdout, `{"type":"usage","input_tokens":150,"output_tokens":90,"cache_read_tokens":40,"cache_write_tokens":10,"cost":0.0002}`)
		emit(stdout, `{"type":"result","status":"completed","text":"done"}`)
		os.Exit(0)

	case "resume":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"message","role":"assistant","delta":"resumed and finishing"}`)
		emit(stdout, `{"type":"file","path":"src/resumed.txt","action":"modified"}`)
		emit(stdout, `{"type":"result","status":"completed","text":"resumed done"}`)
		os.Exit(0)

	case "timeout", "cancellation":
		// Emit init then hang until the adapter cancels/kills the group. A
		// long ticker keeps the runtime's all-goroutines-asleep deadlock
		// detector quiet (a bare select{} would panic the stub), while still
		// blocking forever so only cancellation/timeout ends the run.
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		ticker := time.NewTicker(time.Hour)
		for {
			<-ticker.C
		}

	case "unknown-event":
		emit(stdout, `{"type":"system","subtype":"init","session_id":"grok-session-1"}`)
		emit(stdout, `{"type":"agent.vibes","feeling":"confident"}`) // unknown future item
		emit(stdout, `{"type":"result","status":"completed","text":"done"}`)
		os.Exit(0)

	default:
		fmt.Fprintf(stderr, "unknown stub scenario: %q\n", scenario)
		os.Exit(1)
	}
}

func emit(w io.Writer, line string) {
	fmt.Fprintln(w, line)
}

// emitRaw writes a line without ensuring it is valid JSON (used by malformed).
func emitRaw(w io.Writer, line string) {
	fmt.Fprintln(w, line)
}
