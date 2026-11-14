package codeshot

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/inth3shadows/codeshot/internal/codegraph"
)

// Version is set at release time with -ldflags "-X ...codeshot.Version=x.y.z".
var Version = ""

// versionString is the stamped Version, else the module version `go install
// ...@vX.Y.Z` records, else "dev": an unstamped build never claims a release.
func versionString() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

const (
	defaultLimit      = 50
	defaultNodeBudget = 200 // --max-depth-nodes: caps sequential codegraph calls at --depth > 1
	defaultMaxSymbols = 500
)

const usage = `Usage: codeshot <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--format <fmt>] [--depth <n>] [--max-depth-nodes <n>] [--embed <file.md> [--check]]
   or: codeshot --architecture [--path <repoPath>] [--out <file.png>] [--max-render <n>] [--max-symbols <n>] [--group-depth <n>] [--forbid <from->to>]... [--format <fmt>] [--embed <file.md> [--check]]
   or: codeshot --diff [--diff-ref <range>] [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--max-symbols <n>] [--format <fmt>] [--embed <file.md> [--check]]`

type optSpec struct {
	boolean, multiple bool
}

var options = map[string]optSpec{
	"version": {boolean: true}, "path": {}, "out": {}, "limit": {}, "max-render": {},
	"format": {}, "depth": {}, "max-depth-nodes": {}, "architecture": {boolean: true},
	"max-symbols": {}, "group-depth": {}, "diff": {boolean: true}, "diff-ref": {},
	"embed": {}, "check": {boolean: true}, "forbid": {multiple: true},
}

// parsed holds flag values; a string flag absent from str was not given.
type parsed struct {
	str         map[string]string
	bools       map[string]bool
	forbid      []string
	positionals []string
}

// argError mirrors the two error classes the v0.3.0 CLI distinguished: a bad
// option value (reported per numeric flag) and everything else.
type argError struct {
	invalidValue bool
	flag, msg    string
}

func (e *argError) Error() string { return e.msg }

// parseArgs accepts the same command lines as v0.3.0 (node:util parseArgs,
// strict, positionals allowed): "--flag value", "--flag=value", "-v", "--".
func parseArgs(args []string) (*parsed, error) {
	p := &parsed{str: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			p.positionals = append(p.positionals, args[i+1:]...)
			return p, nil
		case strings.HasPrefix(a, "--"):
			name, val, inline := strings.Cut(a[2:], "=")
			spec, ok := options[name]
			if !ok {
				return nil, &argError{msg: fmt.Sprintf("Unknown option '--%s'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- \"--%s\"'", name, name)}
			}
			if spec.boolean {
				if inline {
					return nil, &argError{msg: fmt.Sprintf("Option '--%s' does not take an argument", name)}
				}
				p.bools[name] = true
				continue
			}
			if !inline {
				if i+1 >= len(args) {
					return nil, &argError{invalidValue: true, flag: name, msg: fmt.Sprintf("Option '--%s <value>' argument missing", name)}
				}
				val = args[i+1]
				if len(val) > 1 && val[0] == '-' {
					return nil, &argError{invalidValue: true, flag: name, msg: fmt.Sprintf("Option '--%s' argument is ambiguous.\nDid you forget to specify the option argument for '--%s'?\nTo specify an option argument starting with a dash use '--%s=-XYZ'.", name, name, name)}
				}
				i++
			}
			if spec.multiple {
				p.forbid = append(p.forbid, val)
			} else {
				p.str[name] = val
			}
		case len(a) > 1 && a[0] == '-':
			for _, c := range a[1:] {
				if c != 'v' {
					return nil, &argError{msg: fmt.Sprintf("Unknown option '-%c'. To specify a positional argument starting with a '-', place it at the end of the command after '--', as in '-- \"-%c\"'", c, c)}
				}
				p.bools["version"] = true
			}
		default:
			p.positionals = append(p.positionals, a)
		}
	}
	return p, nil
}

// jsNumber converts the way JavaScript's Number() does for the inputs a flag
// realistically gets, so values v0.3.0 accepted ("1e2", " 5 ") still parse.
func jsNumber(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	if strings.ContainsAny(t, "_") {
		return math.NaN()
	}
	lower := strings.ToLower(t)
	for prefix, base := range map[string]int{"0x": 16, "0o": 8, "0b": 2} {
		if strings.HasPrefix(lower, prefix) {
			n, err := strconv.ParseInt(t[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	switch t {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if strings.ContainsAny(lower, "inftyp") { // Go accepts inf/nan/hex floats; JS doesn't
		return math.NaN()
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// positiveInt validates a positive-integer flag value.
func positiveInt(flag, raw string) (int, error) {
	f := jsNumber(raw)
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f <= 0 || f > 1<<53 {
		return 0, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --%s must be a positive integer, got '%s'", flag, raw)}
	}
	return int(f), nil
}

func requireOnPath(bin, hint string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: '%s' not found on PATH. %s", bin, hint)}
	}
	return nil
}

var numericFlags = []string{"limit", "max-render", "depth", "max-depth-nodes", "max-symbols", "group-depth"}

// Main runs codeshot and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	code, err := run(args, stdout, stderr)
	if err == nil {
		return code
	}
	var ex *Exit
	var fatal *codegraph.FatalError
	switch {
	case errors.As(err, &ex):
		if ex.Stdout != "" {
			fmt.Fprintln(stdout, ex.Stdout)
		}
		if ex.Stderr != "" {
			fmt.Fprintln(stderr, ex.Stderr)
		}
		return ex.Code
	case errors.As(err, &fatal):
		fmt.Fprintln(stderr, fatal.Msg)
		return 1
	default:
		fmt.Fprintf(stderr, "codeshot: %s\n", err.Error())
		return 1
	}
}

func usageExit(msg string) error {
	return &Exit{Code: 1, Stderr: msg + "\n" + usage}
}

func run(args []string, stdout, stderr io.Writer) (int, error) {
	p, err := parseArgs(args)
	if err != nil {
		var ae *argError
		if errors.As(err, &ae) && ae.invalidValue {
			for _, f := range numericFlags {
				if ae.flag == f {
					bad := "undefined"
					for i, a := range args {
						if a == "--"+f && i+1 < len(args) {
							bad = args[i+1]
						}
					}
					return 0, usageExit(fmt.Sprintf("codeshot: --%s must be a positive integer, got '%s'", f, bad))
				}
			}
		}
		return 0, usageExit("codeshot: " + err.Error())
	}
	if p.bools["version"] {
		fmt.Fprintf(stdout, "codeshot %s\n", versionString())
		return 0, nil
	}

	symbol := ""
	if len(p.positionals) > 0 {
		symbol = p.positionals[0]
	}
	arch := p.bools["architecture"]
	diffRef, hasDiffRef := p.str["diff-ref"]
	diffMode := p.bools["diff"] || hasDiffRef
	otherMode := "--diff"
	if arch {
		otherMode = "--architecture"
	}
	if arch && diffMode {
		return 0, usageExit("codeshot: --architecture cannot be combined with --diff")
	}
	if (arch || diffMode) && symbol != "" {
		m := "diff"
		if arch {
			m = "architecture"
		}
		return 0, usageExit(fmt.Sprintf("codeshot: --%s cannot be combined with a <symbol> argument", m))
	}
	if !arch && !diffMode && symbol == "" {
		return 0, usageExit("codeshot: missing required <symbol> argument")
	}
	if hasDiffRef && diffRef == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --diff-ref must not be empty"}
	}
	repoPath, ok := p.str["path"]
	if !ok {
		repoPath = "."
	}
	if repoPath == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --path must not be empty"}
	}
	outFile, hasOut := p.str["out"]
	if hasOut && outFile == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --out must not be empty"}
	}
	get := func(name, def string) string {
		if v, ok := p.str[name]; ok {
			return v
		}
		return def
	}
	limit, err := positiveInt("limit", get("limit", strconv.Itoa(defaultLimit)))
	if err != nil {
		return 0, err
	}
	if _, given := p.str["limit"]; given && arch {
		// A warning, not a refusal like --depth: v0.3.0 configs pass --limit
		// here, and they still get the same diagram.
		warn(stderr, "codeshot: --limit has no effect with --architecture (every callee is read from codegraph's index); ignoring it.")
	}
	maxRender := Unlimited
	if v, ok := p.str["max-render"]; ok {
		if maxRender, err = positiveInt("max-render", v); err != nil {
			return 0, err
		}
	}
	format := get("format", "png")
	if format == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --format must not be empty"}
	}
	depthRaw := get("depth", "1")
	depth, err := positiveInt("depth", depthRaw)
	if err != nil {
		return 0, err
	}
	if (arch || diffMode) && depthRaw != "1" {
		reason := "diff mode only draws direct callers/callees around each changed symbol"
		if arch {
			reason = "there is no multi-hop file traversal"
		}
		return 0, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --depth has no effect with %s (%s)", otherMode, reason)}
	}
	maxDepthNodes := defaultNodeBudget
	if v, ok := p.str["max-depth-nodes"]; ok {
		if maxDepthNodes, err = positiveInt("max-depth-nodes", v); err != nil {
			return 0, err
		}
		if (arch || diffMode) && maxDepthNodes != defaultNodeBudget {
			return 0, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --max-depth-nodes has no effect with %s (there is no multi-hop traversal)", otherMode)}
		}
	}
	maxSymbols, err := positiveInt("max-symbols", get("max-symbols", strconv.Itoa(defaultMaxSymbols)))
	if err != nil {
		return 0, err
	}
	groupDepth := 0
	if v, ok := p.str["group-depth"]; ok {
		if groupDepth, err = positiveInt("group-depth", v); err != nil {
			return 0, err
		}
		if !arch {
			reason := "a symbol trail has no file-level graph to roll up"
			if diffMode {
				reason = "--diff diagrams individual changed symbols, not files, so there is no file-level graph to roll up"
			}
			return 0, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --group-depth only applies with --architecture (%s)", reason)}
		}
	}
	// Parsed before any codegraph work, so a typo fails fast.
	var rules []ForbidRule
	for _, raw := range p.forbid {
		r, ok := ParseForbidRule(raw)
		if !ok {
			return 0, &Exit{Code: 1, Stderr: fmt.Sprintf("codeshot: --forbid expects FROM->TO (e.g. 'render/->test/'), got '%s'", raw)}
		}
		rules = append(rules, r)
	}
	if len(rules) > 0 && !arch {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --forbid only applies with --architecture (its rules are about file-to-file edges)"}
	}
	embedFile, hasEmbed := p.str["embed"]
	if hasEmbed && embedFile == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --embed must not be empty"}
	}
	check := p.bools["check"]
	if check && embedFile == "" {
		return 0, &Exit{Code: 1, Stderr: "codeshot: --check only applies with --embed (it verifies an embedded diagram is current)"}
	}
	if embedFile != "" {
		if _, err := os.Stat(embedFile); err != nil {
			return 0, missingEmbed(embedFile)
		}
	}

	safeSymbol := ""
	if !arch && !diffMode {
		safeSymbol = SanitizeForFilename(symbol)
	}
	// A --diff-ref gets its own marker id and image name, so two ranges
	// embedded in one doc don't overwrite each other.
	diffMarkerID := "diff"
	if diffRef != "" {
		diffMarkerID += "-" + SanitizeForFilename(diffRef)
	}
	archMarkerID := "arch"
	if groupDepth != 0 {
		archMarkerID = fmt.Sprintf("arch-d%d", groupDepth)
	}
	if !hasOut {
		var base string
		// #51: no checkout-directory name in an embedded image's default
		// name, or a diagram committed from one clone fails --check in
		// another. F12: a --path below the repo root is named instead, so two
		// docs in one directory diagramming different subtrees don't collide.
		sub := ""
		if embedFile != "" && (arch || diffMode) {
			if s := EmbedPathSuffix(repoPath); s != "" {
				sub = "-" + s
			}
		}
		switch {
		case embedFile != "" && arch:
			base = archMarkerID + sub
		case embedFile != "" && diffMode:
			base = diffMarkerID + sub
		case arch:
			base = archMarkerID + "-" + ArchOutputBaseName(repoPath)
		case diffMode:
			base = diffMarkerID + "-" + ArchOutputBaseName(repoPath)
		default:
			base = "callgraph-" + safeSymbol
		}
		if embedFile != "" {
			// Stable, next to the doc: the link resolves and re-runs overwrite it.
			dir, _ := filepath.Abs(filepath.Dir(embedFile))
			outFile = filepath.Join(dir, fmt.Sprintf("codeshot-%s.%s", base, format))
		} else {
			outFile = filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d.%s", base, time.Now().UnixMilli(), format))
		}
	} else {
		warn(stderr, FormatMismatchWarning(outFile, format))
	}

	if err := requireOnPath("codegraph", "Install: https://github.com/colbymchenry/codegraph"); err != nil {
		return 0, err
	}
	if err := requireOnPath("dot", "Install graphviz (e.g. `brew install graphviz` or `apt install graphviz`)."); err != nil {
		return 0, err
	}
	if diffMode {
		if err := requireOnPath("git", "Install git (e.g. `apt install git`) — --diff shells out to `git diff` to find changed files."); err != nil {
			return 0, err
		}
	}
	cg := codegraph.NewClient()
	warn(stderr, IndexHealthWarning(cg, repoPath))
	tooltips := strings.EqualFold(format, "svg") || strings.EqualFold(format, "svgz")
	out := OutputParams{Format: format, OutFile: outFile, EmbedFile: embedFile, Check: check}

	switch {
	case diffMode:
		dot, err := RunDiffMode(cg, repoPath, DiffParams{DiffRef: diffRef, Limit: limit, MaxSymbols: maxSymbols, MaxRender: maxRender, Tooltips: tooltips, EmbedFile: embedFile, Check: check}, stderr)
		if err != nil {
			return 0, err
		}
		out.MarkerID = diffMarkerID
		out.Alt = "Diff-scoped call graph — generated by codeshot"
		if diffRef != "" {
			out.Alt = fmt.Sprintf("Diff-scoped call graph (%s) — generated by codeshot", diffRef)
		}
		return 0, FinishOutput(dot, out, stdout, stderr)
	case arch:
		dot, violations, err := RunArchitectureMode(cg, repoPath, ArchParams{MaxSymbols: maxSymbols, MaxRender: maxRender, GroupDepth: groupDepth, Forbid: rules}, stderr)
		if err != nil {
			return 0, err
		}
		out.MarkerID = archMarkerID
		out.Alt = "Architecture — generated by codeshot"
		if groupDepth != 0 {
			out.Alt = fmt.Sprintf("Architecture (grouped by directory, depth %d) — generated by codeshot", groupDepth)
		}
		if err := FinishOutput(dot, out, stdout, stderr); err != nil {
			return 0, err
		}
		// The diagram is still written (it is the evidence); the run fails after.
		if len(violations) > 0 {
			return 1, nil
		}
		return 0, nil
	default:
		dot, resolved, err := RunSymbolMode(cg, repoPath, symbol, SymbolParams{Limit: limit, MaxRender: maxRender, Depth: depth, MaxDepthNodes: maxDepthNodes, Tooltips: tooltips}, stderr)
		if err != nil {
			return 0, err
		}
		out.MarkerID = safeSymbol
		out.Alt = resolved + " call graph — generated by codeshot"
		return 0, FinishOutput(dot, out, stdout, stderr)
	}
}
