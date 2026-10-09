// envmask masks values in .env files so they can be shared safely.
// The source file is never modified.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const version = "0.1.0"

const mask = "***"

var (
	// KEY=VALUE, optionally with export and spaces around '='.
	assignRe = regexp.MustCompile(`^(\s*)(export\s+)?([A-Za-z_][A-Za-z0-9_.\-]*)(\s*)=(\s*)(.*)$`)
	// Commented-out assignment: "# KEY=VALUE".
	commentedAssignRe = regexp.MustCompile(`^(\s*#+\s*)(export\s+)?([A-Za-z_][A-Za-z0-9_.\-]*)(\s*)=(.*)$`)
	// Value consisting entirely of ${VAR} / $VAR references.
	pureRefRe   = regexp.MustCompile(`^(\$\{[A-Za-z_][A-Za-z0-9_]*(:?[-=?+][^}]*)?\}|\$[A-Za-z_][A-Za-z0-9_]*)+$`)
	urlSchemeRe = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.\-]*)://`)
	intRe       = regexp.MustCompile(`^[+-]?\d+$`)
	floatRe     = regexp.MustCompile(`^[+-]?(\d+\.\d*|\.\d+)([eE][+-]?\d+)?$`)
	durationRe  = regexp.MustCompile(`^\d+(\.\d+)?(ns|us|µs|ms|s|m|h|d|w)$`)
	plainWordRe = regexp.MustCompile(`^[a-z_\-]+$`)
	emailRe     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	placeholder = regexp.MustCompile(`(?i)^(` +
		`change[_\-]?me|changeit|replace[_\-]?me|todo|tbd|fixme|placeholder|example|sample|dummy|` +
		`secret|password|token|null|nil|none|undefined|` +
		`x{3,}|\*{3,}|\.{3,}|-+|` +
		`your[_\- ].*|<.*>|\[.*\]|\{\{.*\}\}|` +
		`.*[_\-](here|goes[_\-]here)` +
		`)$`)
)

type options struct {
	types        bool // show the value type instead of ***
	keepComments bool
}

type stats struct {
	vars, masked, empty, placeholders, refs, unparsed int
	secrets                                           []string // original values for the final leak check
}

func main() {
	var (
		out     = flag.String("o", "", "where to write the result (default: <file>.masked next to it; \"-\" = stdout)")
		types   = flag.Bool("t", false, "show the value type instead of ***: <url:postgres>, <bool>, <int>, <placeholder>…")
		noCom   = flag.Bool("strip-comments", false, "remove all comments (by default regular comments are kept)")
		force   = flag.Bool("f", false, "overwrite the output file if it already exists")
		showVer = flag.Bool("version", false, "print version")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `envmask %s — masks secrets in .env files

Usage:
  envmask [flags] <path to .env>

The source file is not modified. The result is written next to it: .env.production → .env.production.masked

Flags:
`, version)
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  envmask .env.production
  envmask -t .env.production          # show value types
  envmask -t -o - .env | pbcopy       # straight to the clipboard (macOS)
`)
	}
	flag.Parse()

	if *showVer {
		fmt.Println("envmask", version)
		return
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	in := flag.Arg(0)
	if err := run(in, *out, options{types: *types, keepComments: !*noCom}, *force); err != nil {
		fmt.Fprintln(os.Stderr, "envmask:", err)
		os.Exit(1)
	}
}

func run(inPath, outPath string, opt options, force bool) error {
	src, err := os.ReadFile(inPath)
	if err != nil {
		return err
	}

	if outPath == "" {
		outPath = inPath + ".masked"
	}
	toStdout := outPath == "-"

	if !toStdout {
		absIn, _ := filepath.Abs(inPath)
		absOut, _ := filepath.Abs(outPath)
		if absIn == absOut {
			return errors.New("output file is the same as the input file — refusing to overwrite it")
		}
		if !force {
			if _, err := os.Stat(outPath); err == nil {
				return fmt.Errorf("%s already exists (use -f to overwrite)", outPath)
			}
		}
	}

	result, st := maskEnv(string(src), opt)

	// Safety net: no original value may end up in the output.
	if leak := findLeak(result, st.secrets); leak != "" {
		return fmt.Errorf("self-check failed: a fragment of an original value was found in the output (%d chars) — file not written", len(leak))
	}

	if toStdout {
		_, err = io.WriteString(os.Stdout, result)
		return err
	}

	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !force {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(outPath, flags, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, result); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "✓ %s → %s\n  variables: %d, masked: %d, empty: %d",
		inPath, outPath, st.vars, st.masked, st.empty)
	if opt.types {
		fmt.Fprintf(os.Stderr, ", placeholders: %d, refs: %d", st.placeholders, st.refs)
	}
	if st.unparsed > 0 {
		fmt.Fprintf(os.Stderr, "\n  ⚠ unrecognized lines hidden: %d", st.unparsed)
	}
	fmt.Fprintln(os.Stderr)
	return nil
}

// maskEnv parses .env content and returns a masked version.
func maskEnv(src string, opt options) (string, *stats) {
	st := &stats{}
	var b strings.Builder

	sc := bufio.NewScanner(strings.NewReader(src))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimSuffix(sc.Text(), "\r"))
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			b.WriteString(line)

		case strings.HasPrefix(trimmed, "#"):
			// Mask commented-out assignments too: they often hold old keys.
			if m := commentedAssignRe.FindStringSubmatch(line); m != nil {
				val, _ := splitValue(m[5])
				st.remember(val)
				if !opt.keepComments {
					continue
				}
				b.WriteString(m[1])
				b.WriteString(m[2])
				b.WriteString(m[3])
				b.WriteString(m[4])
				b.WriteString("=")
				if val.raw != "" {
					b.WriteString(val.wrap(mask))
				}
			} else if opt.keepComments {
				b.WriteString(line)
			} else {
				continue
			}

		default:
			m := assignRe.FindStringSubmatch(line)
			if m == nil {
				st.unparsed++
				st.remember(value{raw: trimmed})
				fmt.Fprintf(&b, "# [envmask: line %d not recognized and hidden]", i+1)
				break
			}
			indent, export, key, sp1, sp2, rest := m[1], m[2], m[3], m[4], m[5], m[6]

			// Multi-line quoted value: collect lines up to the closing quote.
			if q := quoteOf(rest); (q == '"' || q == '\'' || q == '`') && !closesQuote(rest[1:], q) {
				j := i + 1
				for ; j < len(lines); j++ {
					rest += "\n" + lines[j]
					if closesQuote(lines[j], q) {
						break
					}
				}
				i = min(j, len(lines)-1)
			}

			val, comment := splitValue(rest)
			st.vars++
			st.remember(val)

			b.WriteString(indent)
			b.WriteString(export)
			b.WriteString(key)
			b.WriteString(sp1)
			b.WriteString("=")
			b.WriteString(sp2)
			b.WriteString(render(val, opt, st))
			if comment != "" && opt.keepComments {
				b.WriteString(" ")
				b.WriteString(comment)
			}
		}
		b.WriteByte('\n')
	}
	return b.String(), st
}

type value struct {
	raw   string // value without quotes
	quote byte   // 0, '"', '\'' or '`'
}

func (v value) wrap(s string) string {
	if v.quote == 0 {
		return s
	}
	return string(v.quote) + s + string(v.quote)
}

func (st *stats) remember(v value) {
	if len(v.raw) > 3 {
		st.secrets = append(st.secrets, v.raw)
	}
}

func render(v value, opt options, st *stats) string {
	if v.raw == "" {
		st.empty++
		return v.wrap("")
	}
	if !opt.types {
		st.masked++
		return v.wrap(mask)
	}

	s := strings.TrimSpace(v.raw)
	switch {
	case placeholder.MatchString(s):
		st.placeholders++
		return v.wrap("<placeholder>")
	case pureRefRe.MatchString(s):
		st.refs++
		return v.wrap("<ref>")
	}
	st.masked++
	return v.wrap("<" + classify(s) + ">")
}

// classify determines the value type without revealing its contents.
func classify(s string) string {
	lower := strings.ToLower(s)
	switch {
	case lower == "true" || lower == "false" || lower == "yes" || lower == "no" || lower == "on" || lower == "off":
		return "bool"
	case intRe.MatchString(s):
		return "int"
	case floatRe.MatchString(s):
		return "float"
	case durationRe.MatchString(s):
		return "duration"
	}
	if m := urlSchemeRe.FindStringSubmatch(s); m != nil {
		scheme := strings.ToLower(m[1])
		if strings.Contains(s, "@") {
			return "url:" + scheme + "+creds"
		}
		return "url:" + scheme
	}
	if emailRe.MatchString(s) {
		return "email"
	}
	if strings.HasPrefix(s, "-----BEGIN") {
		return "pem"
	}
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) || (strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		return "json"
	}
	if strings.Contains(s, ",") && !strings.ContainsAny(s, " \t") {
		return "list:" + strconv.Itoa(strings.Count(s, ",")+1)
	}
	return "string"
}

func quoteOf(s string) byte {
	if s == "" {
		return 0
	}
	switch s[0] {
	case '"', '\'', '`':
		return s[0]
	}
	return 0
}

// closesQuote reports whether s contains an unescaped closing quote q.
func closesQuote(s string, q byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && q == '"' {
			i++
			continue
		}
		if s[i] == q {
			return true
		}
	}
	return false
}

// splitValue separates the value from an inline comment and strips quotes.
func splitValue(rest string) (value, string) {
	rest = strings.TrimRight(rest, " \t")
	if q := quoteOf(rest); q != 0 {
		for i := 1; i < len(rest); i++ {
			if rest[i] == '\\' && q == '"' {
				i++
				continue
			}
			if rest[i] == q {
				tail := strings.TrimSpace(rest[i+1:])
				comment := ""
				if strings.HasPrefix(tail, "#") {
					comment = tail
				}
				return value{raw: rest[1:i], quote: q}, comment
			}
		}
		return value{raw: rest[1:], quote: q}, "" // unclosed quote
	}
	// Unquoted: a comment starts with " #".
	if idx := strings.Index(rest, " #"); idx >= 0 {
		return value{raw: strings.TrimSpace(rest[:idx])}, strings.TrimSpace(rest[idx:])
	}
	if idx := strings.Index(rest, "\t#"); idx >= 0 {
		return value{raw: strings.TrimSpace(rest[:idx])}, strings.TrimSpace(rest[idx:])
	}
	return value{raw: strings.TrimSpace(rest)}, ""
}

func findLeak(out string, secrets []string) string {
	for _, s := range secrets {
		// Short and common values (true, 3000, production) may legitimately appear in key names/comments.
		if len(s) < 8 || placeholder.MatchString(s) || plainWordRe.MatchString(s) {
			continue
		}
		if strings.Contains(out, s) {
			return s
		}
	}
	return ""
}
