// Command kimistub is a minimal, deterministic emulator of the Kimi Code CLI
// surface, used ONLY by the kimi adapter's tests (rule §36.5: no real paid API
// is ever called). It is co-developed with the adapter: it accepts exactly the
// headless argv the adapter builds and emits recorded Kimi-format stream-json
// byte streams selected by the KIMI_STUB_SCENARIO environment variable.
//
// It is NOT the real Kimi engine and makes no network calls. Build it from the
// kimi package tests and place it on PATH (or pass it via Options.BinaryOverride)
// to exercise detection, version parsing, the streaming parser, cancellation,
// timeouts and the conformance suite offline.
//
// Usage:
//
//	kimistub --version                                 # detection probe
//	kimistub --help                                    # flag probe
//	kimistub -p <prompt> [--output stream-json] [--model M] [--continue SID] [--max-turns N]
//
// Environment:
//
//	KIMI_STUB_SCENARIO  success|quota-before-edits|malformed-json|timeout|
//	                    cancellation|crash|partial-output|resume|rate-limit|
//	                    auth-failure|scope-violation|usage-events|flag-error
//	KIMI_STUB_VERSION   override the --version string (default "Kimi Code 1.4.0")
//	KIMI_STUB_BOM       when non-empty, prefix the stream with a UTF-8 BOM
//	KIMI_STUB_CRLF      when non-empty, terminate lines with CRLF
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	// Parse known flags tolerantly: the adapter only emits the flags below; any
	// unknown token is consumed as a value so an unexpected flag does not abort
	// parsing (mirrors how a forgiving CLI behaves).
	fs := flag.NewFlagSet("kimistub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		printVersion bool
		printHelp    bool
		prompt       string
		output       string
		model        string
		cont         string
		maxTurns     int
	)
	fs.BoolVar(&printVersion, "version", false, "print version and exit")
	fs.BoolVar(&printHelp, "help", false, "print help and exit")
	fs.StringVar(&prompt, "p", "", "prompt")
	fs.StringVar(&output, "output", "", "output format")
	fs.StringVar(&model, "model", "", "model id")
	fs.StringVar(&cont, "continue", "", "session id to resume")
	fs.IntVar(&maxTurns, "max-turns", 0, "max turns")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = maxTurns

	if printVersion {
		fmt.Fprintln(stdout, versionString())
		return 0
	}
	if printHelp {
		fmt.Fprintln(stdout, helpText())
		return 0
	}

	scenario := os.Getenv("KIMI_STUB_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}
	emitter := newEmitter(stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return playScenario(ctx, emitter, stdout, stderr, scenario, model, cont, output)
}

func versionString() string {
	if os.Getenv("KIMI_STUB_OLD") != "" {
		return "Kimi Code 0.5.0"
	}
	if v := os.Getenv("KIMI_STUB_VERSION"); v != "" {
		return v
	}
	return "Kimi Code 1.4.0"
}

func helpText() string {
	if os.Getenv("KIMI_STUB_OLD") != "" {
		// Old version: no streaming/resume/max-turns flags.
		return "Kimi Code - coding agent\n" +
			"\n" +
			"Usage: kimi -p <prompt> [options]\n" +
			"\n" +
			"Options:\n" +
			"  -p <prompt>   prompt to run\n" +
			"  --model <id>  model to target\n" +
			"  --version     print version\n"
	}
	return "Kimi Code - coding agent\n" +
		"\n" +
		"Usage: kimi -p <prompt> [options]\n" +
		"\n" +
		"Options:\n" +
		"  -p <prompt>            prompt to run\n" +
		"  --output <format>      output format (stream-json)\n" +
		"  --model <id>           model to target\n" +
		"  --continue <session>   resume a session\n" +
		"  --max-turns <n>        limit agent turns\n" +
		"  --version              print version\n" +
		"  --help                 print help\n"
}

// emitter writes JSONL lines, applying the optional UTF-8 BOM prefix and CRLF
// line endings configured via the KIMI_STUB_BOM / KIMI_STUB_CRLF env vars.
type emitter struct {
	w     io.Writer
	bom   bool
	crlf  bool
	first bool
}

func newEmitter(w io.Writer) *emitter {
	return &emitter{
		w:     w,
		bom:   os.Getenv("KIMI_STUB_BOM") != "",
		crlf:  os.Getenv("KIMI_STUB_CRLF") != "",
		first: true,
	}
}

func (e *emitter) line(s string) {
	out := s
	if e.first && e.bom {
		// Prefix the very first emitted bytes with a UTF-8 BOM.
		out = "\ufeff" + out
	}
	e.first = false
	if e.crlf {
		out += "\r\n"
	} else {
		out += "\n"
	}
	io.WriteString(e.w, out)
}

// json literals for recorded stream-json items, with %s placeholders for the
// model and session id so the scenario echoes the request.
func initItem(model, sid string) string {
	if sid != "" {
		return fmt.Sprintf(`{"type":"system","event":"resume","session_id":%q,"model":%q}`, sid, model)
	}
	sid = "kimi-stub-session"
	return fmt.Sprintf(`{"type":"system","event":"init","session_id":%q,"model":%q,"cwd":%q}`, sid, model, cwd())
}

func textItem(text string) string {
	return fmt.Sprintf(`{"type":"assistant","event":"text","text":%q}`, text)
}

func usageItem(in, out int) string {
	return fmt.Sprintf(`{"type":"usage","input_tokens":%d,"output_tokens":%d,"cost":0.0001,"currency":"USD"}`, in, out)
}

func resultSuccess(model string) string {
	return fmt.Sprintf(`{"type":"result","event":"success","session_id":"kimi-stub-session","model":%q,"usage":{"input_tokens":120,"output_tokens":80,"cost":0.0001}}`, model)
}

func resultError(class, msg string) string {
	return fmt.Sprintf(`{"type":"result","event":"error","class":%q,"error":%q}`, class, msg)
}

func cwd() string {
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return ""
}

// playScenario emits the recorded byte stream for the requested scenario and
// returns the process exit code. Hang scenarios block on ctx until killed.
func playScenario(ctx context.Context, e *emitter, stdout, stderr io.Writer, scenario, model, cont, output string) int {
	streaming := output == "stream-json"

	switch scenario {
	case "flag-error":
		// Simulates the engine rejecting a flag: emit nothing, error on stderr.
		fmt.Fprintln(stderr, "kimi: unknown flag: --foo")
		return 2

	case "crash":
		if streaming {
			e.line(initItem(model, ""))
		}
		fmt.Fprintln(stderr, "kimi agent panicked (simulated crash)")
		return 134

	case "partial-output":
		if streaming {
			e.line(initItem(model, ""))
			e.line(textItem("partial..."))
		}
		return 1

	case "timeout", "cancellation":
		if streaming {
			e.line(initItem(model, ""))
		}
		// Block until the supervisor kills the process group.
		<-ctx.Done()
		return 137

	case "quota-before-edits":
		if streaming {
			e.line(initItem(model, ""))
			e.line(usageItem(0, 0))
			e.line(resultError("PROVIDER_QUOTA", "quota exhausted"))
		}
		fmt.Fprintln(stderr, "error: quota exhausted before any edits")
		return 2

	case "rate-limit":
		if streaming {
			e.line(initItem(model, ""))
			e.line(resultError("PROVIDER_RATE_LIMIT", "HTTP 429 too many requests"))
		}
		fmt.Fprintln(stderr, "HTTP 429 too many requests")
		return 2

	case "auth-failure":
		if streaming {
			e.line(initItem(model, ""))
			e.line(resultError("PROVIDER_AUTH", "401 unauthorized: invalid api key"))
		}
		fmt.Fprintln(stderr, "401 unauthorized: invalid api key")
		return 2

	case "scope-violation":
		if streaming {
			e.line(initItem(model, ""))
			e.line(`{"type":"file","path":"OUTSIDE_SCOPE/secret.txt","action":"created"}`)
			e.line(resultError("SCOPE_VIOLATION", "scope violation: wrote outside allowed paths"))
		}
		fmt.Fprintln(stderr, "scope violation: wrote outside allowed paths")
		return 2

	case "usage-events":
		if streaming {
			e.line(initItem(model, ""))
			e.line(usageItem(100, 50))
			e.line(usageItem(150, 90))
			e.line(resultSuccess(model))
		}
		return 0

	case "malformed-json":
		if streaming {
			e.line(initItem(model, ""))
			e.line("{not valid json")
			e.line(textItem("still working"))
			e.line(resultSuccess(model))
		}
		return 0

	case "resume":
		if streaming {
			e.line(initItem(model, nonEmpty(cont, "kimi-stub-session")))
			e.line(textItem("resumed and finishing"))
			e.line(resultSuccess(model))
		}
		return 0

	case "success", "":
		if streaming {
			e.line(initItem(model, ""))
			e.line(textItem("Hello from Kimi"))
			e.line(`{"type":"file","path":"src/hello.txt","action":"created"}`)
			e.line(usageItem(120, 80))
			e.line(resultSuccess(model))
		} else {
			io.WriteString(stdout, "Hello from Kimi\n")
		}
		return 0

	default:
		fmt.Fprintf(stderr, "kimistub: unknown scenario %q\n", scenario)
		return 1
	}
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
