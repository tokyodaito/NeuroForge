package declarative

import (
	"fmt"
	"strconv"
	"strings"
)

// Manifest is the declarative adapter description (spec §13.1).
type Manifest struct {
	APIVersion   string           `yaml:"api_version"` // "neuroforge/v1"
	Kind         string           `yaml:"kind"`        // "command-coding-agent"
	ID           string           `yaml:"id"`
	Detect       CommandSpec      `yaml:"detect"`
	Run          CommandSpec      `yaml:"run"`
	Capabilities CapabilitiesYAML `yaml:"capabilities"`
	// Scenario is an optional fake/conformance hook (ignored by real adapters).
	Scenario string `yaml:"scenario"`
}

// CommandSpec is a detect/run command (spec §13.1). Command is the argv list,
// with "{{ template }}" placeholders substituted at run time.
type CommandSpec struct {
	Command []string `yaml:"command"`
}

// CapabilitiesYAML mirrors [protocol.AgentCapabilities] in snake_case YAML.
type CapabilitiesYAML struct {
	InteractiveMode      bool `yaml:"interactive_mode"`
	HeadlessMode         bool `yaml:"headless_mode"`
	StreamingEvents      bool `yaml:"streaming_events"`
	StructuredOutput     bool `yaml:"structured_output"`
	ImageInput           bool `yaml:"image_input"`
	SessionResume        bool `yaml:"session_resume"`
	LiveUserMessages     bool `yaml:"live_user_messages"`
	ModelSelection       bool `yaml:"model_selection"`
	UsageReporting       bool `yaml:"usage_reporting"`
	CachedUsageReporting bool `yaml:"cached_usage_reporting"`
	ToolPermissions      bool `yaml:"tool_permissions"`
	NativeSandbox        bool `yaml:"native_sandbox"`
	MCP                  bool `yaml:"mcp"`
	ACP                  bool `yaml:"acp"`
}

// ParseManifest parses a declarative adapter manifest from YAML bytes. It uses a
// minimal YAML subset parser (nested maps, block sequences, scalars) sufficient
// for the §13.1 grammar; for richer manifests a JSON-encoded Manifest may be
// supplied instead via [ParseManifestJSON].
func ParseManifest(data []byte) (Manifest, error) {
	root, err := parseYAML(string(data))
	if err != nil {
		return Manifest{}, err
	}
	m, ok := root.(map[string]any)
	if !ok {
		return Manifest{}, fmt.Errorf("declarative: manifest must be a YAML mapping, got %T", root)
	}
	var out Manifest
	if err := decodeManifest(m, &out); err != nil {
		return Manifest{}, err
	}
	if out.ID == "" {
		return Manifest{}, fmt.Errorf("declarative: manifest missing required field \"id\"")
	}
	return out, nil
}

// decodeManifest maps the generic parsed tree onto the typed Manifest.
func decodeManifest(m map[string]any, out *Manifest) error {
	out.APIVersion = getString(m, "api_version")
	out.Kind = getString(m, "kind")
	out.ID = getString(m, "id")
	out.Scenario = getString(m, "scenario")

	if v, ok := m["detect"]; ok {
		if err := decodeCommand(v, &out.Detect); err != nil {
			return fmt.Errorf("detect: %w", err)
		}
	}
	if v, ok := m["run"]; ok {
		if err := decodeCommand(v, &out.Run); err != nil {
			return fmt.Errorf("run: %w", err)
		}
	}
	if v, ok := m["capabilities"]; ok {
		if err := decodeCaps(v, &out.Capabilities); err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
	}
	return nil
}

func decodeCommand(v any, out *CommandSpec) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a mapping, got %T", v)
	}
	cmd, ok := m["command"]
	if !ok {
		return fmt.Errorf("missing \"command\"")
	}
	switch c := cmd.(type) {
	case []any:
		out.Command = make([]string, 0, len(c))
		for _, item := range c {
			out.Command = append(out.Command, fmt.Sprint(item))
		}
	case []string:
		out.Command = c
	case string:
		// Allow a single-string command (shell-like); keep it as one token so
		// callers that need argv should use the list form.
		out.Command = []string{c}
	default:
		return fmt.Errorf("command must be a list, got %T", cmd)
	}
	return nil
}

func decodeCaps(v any, out *CapabilitiesYAML) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("must be a mapping, got %T", v)
	}
	out.InteractiveMode = getBool(m, "interactive_mode")
	out.HeadlessMode = getBool(m, "headless_mode")
	out.StreamingEvents = getBool(m, "streaming_events")
	out.StructuredOutput = getBool(m, "structured_output")
	out.ImageInput = getBool(m, "image_input")
	out.SessionResume = getBool(m, "session_resume")
	out.LiveUserMessages = getBool(m, "live_user_messages")
	out.ModelSelection = getBool(m, "model_selection")
	out.UsageReporting = getBool(m, "usage_reporting")
	out.CachedUsageReporting = getBool(m, "cached_usage_reporting")
	out.ToolPermissions = getBool(m, "tool_permissions")
	out.NativeSandbox = getBool(m, "native_sandbox")
	out.MCP = getBool(m, "mcp")
	out.ACP = getBool(m, "acp")
	return nil
}

func getString(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func getBool(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	default:
		return false
	}
}

// ---- minimal YAML subset parser ----

// parseYAML parses the manifest grammar into generic Go values
// (map[string]any / []any / string / bool / int). It supports:
//   - "key: value" pairs (scalar values, optionally quoted);
//   - nested mappings via indentation;
//   - block sequences ("- item");
//   - comments ("# ...") and blank lines.
//
// It does NOT support flow collections, anchors, multi-line scalars or tags.
func parseYAML(s string) (any, error) {
	lines := normalizeYAMLLines(strings.Split(s, "\n"))
	p := &yamlParser{lines: lines}
	v, _, err := p.parseBlock(0)
	return v, err
}

type yamlLine struct {
	indent  int
	content string
}

func normalizeYAMLLines(in []string) []yamlLine {
	var out []yamlLine
	for _, raw := range in {
		// Strip comments (naive: a '#' starting a token; preserve quoted '#').
		trimmed := stripComment(raw)
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		out = append(out, yamlLine{indent: indent, content: strings.TrimSpace(trimmed)})
	}
	return out
}

func stripComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
				return s[:i]
			}
		}
	}
	return s
}

type yamlParser struct {
	lines []yamlLine
	pos   int
}

// parseBlock parses a block at the given minimum indentation, returning the
// value and the number of lines consumed.
func (p *yamlParser) parseBlock(indent int) (any, int, error) {
	if p.pos >= len(p.lines) {
		return nil, 0, nil
	}
	first := p.lines[p.pos]
	if first.indent < indent {
		return nil, 0, nil
	}
	if strings.HasPrefix(first.content, "- ") || first.content == "-" {
		return p.parseSeq(first.indent)
	}
	return p.parseMap(first.indent)
}

func (p *yamlParser) parseMap(indent int) (any, int, error) {
	m := map[string]any{}
	consumed := 0
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			// Belongs to a nested structure handled by recursion.
			return nil, consumed, fmt.Errorf("yaml: unexpected indentation at %q", ln.content)
		}
		key, val, hasInline, err := splitMapEntry(ln.content)
		if err != nil {
			return nil, consumed, err
		}
		p.pos++
		consumed++
		if hasInline {
			m[key] = parseScalar(val)
			continue
		}
		// Nested block: peek next line's indent.
		if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
			child, n, err := p.parseBlock(p.lines[p.pos].indent)
			if err != nil {
				return nil, consumed, err
			}
			_ = n
			m[key] = child
		} else {
			m[key] = nil
		}
	}
	return m, consumed, nil
}

func (p *yamlParser) parseSeq(indent int) (any, int, error) {
	var seq []any
	consumed := 0
	for p.pos < len(p.lines) {
		ln := p.lines[p.pos]
		if ln.indent != indent || !(strings.HasPrefix(ln.content, "- ") || ln.content == "-") {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(ln.content, "-"))
		p.pos++
		consumed++
		if item == "" {
			// Nested block under the dash.
			if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				child, _, err := p.parseBlock(p.lines[p.pos].indent)
				if err != nil {
					return nil, consumed, err
				}
				seq = append(seq, child)
			}
			continue
		}
		// Inline item: could be "key: value" (start of a map) or a scalar.
		if _, _, isMap, _ := isMapEntry(item); isMap {
			// Treat as a single-line map; merge continuation lines with deeper indent.
			m := map[string]any{}
			key, val, hasInline, _ := splitMapEntry(item)
			if hasInline {
				m[key] = parseScalar(val)
			} else if p.pos < len(p.lines) && p.lines[p.pos].indent > indent {
				child, _, err := p.parseBlock(p.lines[p.pos].indent)
				if err != nil {
					return nil, consumed, err
				}
				m[key] = child
			}
			// Continue absorbing same-indent "key: value" lines belonging to this item.
			itemIndent := indent + 2
			for p.pos < len(p.lines) && p.lines[p.pos].indent == itemIndent {
				ln2 := p.lines[p.pos]
				k2, v2, hasInline2, err := splitMapEntry(ln2.content)
				if err != nil {
					return nil, consumed, err
				}
				p.pos++
				consumed++
				if hasInline2 {
					m[k2] = parseScalar(v2)
				}
			}
			seq = append(seq, m)
		} else {
			seq = append(seq, parseScalar(item))
		}
	}
	return seq, consumed, nil
}

// splitMapEntry splits "key: value" into key and value; hasInline is false when
// the line is "key:" (nested block follows).
func splitMapEntry(s string) (key, val string, hasInline bool, err error) {
	idx := indexUnquoted(s, ':')
	if idx < 0 {
		return "", "", false, fmt.Errorf("yaml: expected mapping entry, got %q", s)
	}
	key = strings.TrimSpace(s[:idx])
	rest := strings.TrimSpace(s[idx+1:])
	rest = stripComment(rest)
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return key, "", false, nil
	}
	return key, rest, true, nil
}

func isMapEntry(s string) (string, string, bool, error) {
	idx := indexUnquoted(s, ':')
	if idx < 0 {
		return "", "", false, nil
	}
	return strings.TrimSpace(s[:idx]), strings.TrimSpace(s[idx+1:]), true, nil
}

// indexUnquoted finds the first ':' outside quotes.
func indexUnquoted(s string, target byte) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case target:
			if !inSingle && !inDouble {
				return i
			}
		}
	}
	return -1
}

// parseScalar converts a raw scalar string into a typed value (bool, int, or
// trimmed/unquoted string). It also handles flow sequences ([a, b, c]) so the
// §13.1 shorthand "command: [a, b]" parses identically to the block form.
func parseScalar(s string) any {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '[' && s[len(s)-1] == ']' {
		return parseFlowSeq(s[1 : len(s)-1])
	}
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	case "null", "~", "":
		return ""
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return s
}

// parseFlowSeq splits a flow-sequence body "a, b, c" into []any.
func parseFlowSeq(body string) []any {
	if strings.TrimSpace(body) == "" {
		return []any{}
	}
	parts := splitFlow(body)
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		out = append(out, parseScalar(strings.TrimSpace(p)))
	}
	return out
}

// splitFlow splits on top-level commas (respecting quotes and nested brackets).
func splitFlow(s string) []string {
	var parts []string
	depth := 0
	inSingle, inDouble := false, false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '[', '{':
			if !inSingle && !inDouble {
				depth++
			}
		case ']', '}':
			if !inSingle && !inDouble {
				depth--
			}
		case ',':
			if depth == 0 && !inSingle && !inDouble {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}
