package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"neuroforge/internal/adapter/imageprovider"
	"neuroforge/internal/adapter/imageprovider/conformance"
	"neuroforge/internal/adapter/imageprovider/fake"
	"neuroforge/internal/adapter/imageprovider/protocol"
	"neuroforge/internal/artifacts"
)

// runImageProvider dispatches `forge image-provider <subcommand>` (spec §30).
func (a *App) runImageProvider(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.Err, imageProviderUsage)
		return ExitErr
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return a.imageProviderList(rest)
	case "doctor":
		return a.imageProviderDoctor(rest)
	case "-h", "--help":
		fmt.Fprint(a.Out, imageProviderUsage)
		return ExitOK
	default:
		fmt.Fprintf(a.Err, "%s: unknown image-provider subcommand %q\n\n", a.Name, sub)
		fmt.Fprint(a.Err, imageProviderUsage)
		return ExitErr
	}
}

const imageProviderUsage = `Usage: forge image-provider <subcommand> [flags]

Subcommands:
  list        List registered image providers (§14) (--json)
  doctor      Run the §14 image-provider conformance suite against the fake
              provider, and report each registered provider's health (--json)

Image providers are a separate adapter family from coding agents (spec §14, rule
§36.9). Real image calls (GPT Image, Nano Banana) are opt-in and excluded from
CI (rule §33); the fake provider is always available.
`

// imageProviderList lists registered providers (the daemon wires real ones; in
// the absence of a daemon we report the fake default).
func (a *App) imageProviderList(args []string) int {
	fs := flag.NewFlagSet("image-provider list", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}

	// Build a registry with the fake always present (real providers are
	// registered by the daemon when configured). This mirrors the daemon's
	// startup wiring.
	r := imageprovider.NewRegistry()
	store, err := artifacts.New(a.tempArtifactDir())
	if err == nil {
		r.MustRegister(fake.New(fake.AdapterOptions{Store: store, Installed: true}), 0)
	}

	type entry struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	out := make([]entry, 0, r.Len())
	ctx := context.Background()
	for _, ad := range r.All() {
		h := ad.Health(ctx, protocol.Account{})
		out = append(out, entry{ID: ad.ID(), Status: string(h.Status)})
	}
	if *jsonOut {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintln(a.Out, "IMAGE PROVIDERS (§14)")
		for _, e := range out {
			fmt.Fprintf(a.Out, "  %-20s %s\n", e.ID, e.Status)
		}
	}
	return ExitOK
}

// imageProviderDoctor runs the conformance suite against the fake provider.
func (a *App) imageProviderDoctor(args []string) int {
	fs := flag.NewFlagSet("image-provider doctor", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ExitErr
	}
	store, err := artifacts.New(a.tempArtifactDir())
	if err != nil {
		fmt.Fprintf(a.Err, "%s: artifact store: %v\n", a.Name, err)
		return ExitErr
	}
	suite := &conformance.Suite{
		Factory: func(context.Context) (imageprovider.Adapter, func(), error) {
			return fake.New(fake.AdapterOptions{Store: store, Installed: true}), func() {}, nil
		},
	}
	results := suite.Run(context.Background())
	if *jsonOut {
		b, _ := json.MarshalIndent(results, "", "  ")
		fmt.Fprintln(a.Out, string(b))
	} else {
		fmt.Fprintln(a.Out, "NeuroForge image-provider conformance (spec §14)")
		fmt.Fprintln(a.Out)
		for _, r := range results {
			status := "PASS"
			if !r.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(a.Out, "  [%s]  %-30s %s\n", status, r.Name, r.Detail)
		}
		passed, total := conformance.Summary(results)
		fmt.Fprintf(a.Out, "\n%d/%d checks passed\n", passed, total)
	}
	passed, total := conformance.Summary(results)
	if passed != total {
		return ExitErr
	}
	return ExitOK
}

// tempArtifactDir returns the artifacts dir from the resolved daemon dirs.
func (a *App) tempArtifactDir() string {
	d, err := a.resolveDirs()
	if err != nil {
		return ""
	}
	return d.ArtifactsDir
}
