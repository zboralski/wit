// Command wit keeps claims tied to the thing that proves them.
//
// In a malware review, a permission count, string table, hash, decompiled
// symbol, certificate domain, or IOC list should not be copied from memory into
// prose; it should be regenerated from the sample, code, API, or dataset that
// produced it. The same rule applies to ordinary documentation: code, data,
// output, and explanation must move together, or the document starts lying. The
// command stays in the document, so the evidence is reproducible a year later.
//
// The name is wit., the standard legal abbreviation for witness: a wit directive
// embedded in a document is that witness. It says how to regenerate a claim,
// table, count, code block, or markdown region, and `wit check` fails if the
// checked-in text has drifted from that command's output.
//
// One directive namespace, all valid HTML comments:
//
//	<!-- wit: COMMAND -->            run COMMAND; the following block is its output
//	<!-- wit:set NAME COMMAND -->    run COMMAND once; store its scalar stdout
//	<!-- wit:get NAME -->value<!-- wit:end NAME -->   render a variable, visibly
//
// A wit: command targets either a fenced block (the ``` block after it) or a
// region (content up to a bare <!-- wit:end -->). Variables capture a command's
// output once and render it visibly inline; the value lives in the document, not
// in a hidden state file.
//
//	wit check FILE...      exits nonzero if any witnessed text differs from source
//	wit generate FILE...   reruns the commands and rewrites witnessed text to match
//
// It is a dev tool: commands run with your shell's full privileges (like make),
// from the markdown file's directory, captured stdout only.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// usage is the command-line help, printed on misuse and witnessed by the README.
const usage = `usage: wit <check|generate> FILE...

  wit generate FILE...   run the witnessed commands, rewrite the blocks they own
  wit check FILE...      run the same commands, fail if a block has drifted`

func main() {
	// Bare `wit`, `wit -h`, or `wit help` prints usage to stdout and exits 0: it
	// is a help request, not misuse. Anything else missing args is an error.
	if len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help") {
		fmt.Println(usage)
		return
	}
	if len(os.Args) < 2 {
		fmt.Println(usage)
		return
	}
	mode := os.Args[1]
	if mode != "check" && mode != "generate" {
		fmt.Fprintf(os.Stderr, "wit: unknown command %q (want check or generate)\n%s\n", mode, usage)
		os.Exit(2)
	}
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "wit: %s needs at least one FILE\n%s\n", mode, usage)
		os.Exit(2)
	}

	failed := false
	for _, file := range os.Args[2:] {
		res, err := process(file, mode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wit: %s: %v\n", file, err)
			failed = true
			continue
		}
		// UNAVAILABLE is distinct from STALE and from a clean run: a command could
		// not produce evidence, so the cached text was preserved, not reverified.
		// It is never OK; exit nonzero in both modes.
		if len(res.unavail) > 0 {
			fmt.Printf("%s: %d witness(es) UNAVAILABLE; cached text preserved, not reverified\n", file, len(res.unavail))
			failed = true
			continue
		}
		switch {
		case mode == "check" && res.drift > 0:
			fmt.Printf("%s: %d witness(es) STALE\n", file, res.drift)
			failed = true
		case mode == "check":
			fmt.Printf("%s: all witnesses reproduce\n", file)
		case mode == "generate" && res.changed > 0:
			fmt.Printf("%s: %d witness(es) updated\n", file, res.changed)
		case mode == "generate":
			fmt.Printf("%s: up to date\n", file)
		}
	}
	if failed {
		os.Exit(1)
	}
}

const (
	dirOpen  = "<!-- "
	dirClose = " -->"
	witEnd   = "<!-- wit:end -->" // bare close for a region target
)

// cmdTimeout bounds how long a single wit command may run. A documented snippet
// that hangs (an accidental infinite loop, a command that reads stdin) would
// otherwise freeze a CI worker indefinitely; instead it fails after this. It is a
// var, not a const, only so tests can shorten it.
var cmdTimeout = 60 * time.Second

// directive is one parsed wit comment and where it sits in the line slice.
type directive struct {
	kind    string // "cmd", "set", "get"
	name    string // variable name (set/get); empty for cmd
	command string // shell command (cmd/set); empty for get
	line    int    // 0-based index in lines, for error messages
}

// process scans one markdown file. In check mode it reports how many witnesses
// drift; in generate mode it rewrites stale witnesses in place and reports how
// many changed. A structural error (malformed directive, unterminated region,
// unknown or duplicate variable) fails the whole file rather than writing partially.
//
// A witness whose command fails (API down, tool missing, timeout) is UNAVAILABLE,
// not STALE and not a structural error: the cached witnessed text is the last
// evidence, so it is preserved and the file is left byte-for-byte unchanged. Any
// unavailable witness aborts the whole-file write (all-or-nothing), so generate
// never produces a half-fresh, half-stale document.
type result struct {
	changed int      // witnesses rewritten (generate)
	drift   int      // witnesses that ran and differ (check)
	unavail []string // commands that could not run, for reporting
}

func process(file, mode string) (result, error) {
	var res result
	raw, err := os.ReadFile(file)
	if err != nil {
		return res, err
	}
	dir := filepath.Dir(file)
	// Normalize CRLF only for comparison, so a Windows-edited file is matched
	// against command output (which uses \n) instead of carrying a stray \r. The
	// normalized form is NOT written back unless a witness body actually changed
	// (see the write decision below): wit must not rewrite bytes while reporting
	// "up to date".
	norm := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(norm, "\n")

	vars := map[string]string{}      // variable name -> rendered value
	unavailVars := map[string]bool{} // variable whose wit:set command failed
	var out []string
	i := 0
	for i < len(lines) {
		line := lines[i]

		// A fenced code block reached here is NOT a wit: target (targets are
		// consumed by the cmd case via scanFenced). Copy it through verbatim so
		// directive-looking lines inside it (e.g. documenting wit's own syntax)
		// are inert, the same way go generate ignores generator lines in strings.
		// Fence length matters: a four-backtick demo fence must span its inner
		// three-backtick examples, so close only on a fence at least as long as
		// the opener (CommonMark's rule).
		if open := fenceLen(line); open > 0 {
			out = append(out, line)
			i++
			for i < len(lines) && fenceLen(lines[i]) < open {
				out = append(out, lines[i])
				i++
			}
			if i < len(lines) {
				out = append(out, lines[i]) // closing fence (>= opener length)
				i++
			}
			continue
		}

		// Strict standalone directive: if the whole line is a single HTML comment
		// with a wit DIRECTIVE shape (wit:, "wit ", or exactly wit:end), it MUST
		// parse as a valid directive. A wit comment is executable evidence; a
		// malformed one fails loudly. An ordinary comment that merely starts with
		// the letters "wit" (with, witness, witfoo) is not a directive shape and
		// stays inert prose. (htmlComment is false for a line that also contains an
		// inline get span, so those fall to handleGetLine below and stay literal.)
		if body, isComment := htmlComment(line); isComment && looksLikeWitDirective(body) {
			if _, ok := parseDirective(line, i); !ok && strings.TrimSpace(line) != witEnd {
				return res, fmt.Errorf("line %d: malformed wit directive %q "+
					"(want wit: CMD, wit:set NAME CMD, wit:get NAME, or wit:end)", i+1, strings.TrimSpace(line))
			}
		}

		// A wit:get span is inline: the open and close markers, and the value
		// between them, are all on the same line. Handle it before whole-line
		// directives.
		if strings.Contains(line, "<!-- wit:get ") {
			newLine, c, d, gerr := handleGetLine(line, vars, unavailVars, mode)
			if gerr != nil {
				return res, fmt.Errorf("line %d: %w", i+1, gerr)
			}
			res.changed += c
			res.drift += d
			out = append(out, newLine)
			i++
			continue
		}

		d, ok := parseDirective(line, i)
		if !ok {
			out = append(out, line)
			i++
			continue
		}

		switch d.kind {
		case "set":
			// A name is a duplicate whether the prior set succeeded (in vars) or
			// was unavailable (in unavailVars): the variable already exists.
			if _, dup := vars[d.name]; dup || unavailVars[d.name] {
				return res, fmt.Errorf("line %d: duplicate wit:set %q", i+1, d.name)
			}
			val, runErr := run(d.command, dir)
			if runErr != nil {
				// UNAVAILABLE: the command could not produce evidence. The variable
				// EXISTS (the set is there); its source is just unavailable. Mark it
				// so a later wit:get NAME preserves its cached value instead of
				// failing as an undefined variable. Preserve the document.
				res.unavail = append(res.unavail, d.command)
				unavailVars[d.name] = true
				fmt.Printf("  UNAVAILABLE: %s\n    %v\n", d.command, runErr)
				out = append(out, line)
				i++
				continue
			}
			// Variables are scalar: a multi-line value cannot round-trip through a
			// single-line wit:get span, so reject it rather than silently producing
			// a document that stops witnessing the value after one generate.
			if strings.Contains(val, "\n") {
				return res, fmt.Errorf("line %d: wit:set %q produced multiple lines; variables must be scalar (single line)", i+1, d.name)
			}
			vars[d.name] = val
			out = append(out, line) // the set directive stays in the document
			i++

		case "cmd":
			out = append(out, line) // keep the wit: comment
			i++
			if i >= len(lines) {
				return res, fmt.Errorf("line %d: wit: directive %q has no target block", d.line+1, d.command)
			}
			// Scan the target first (structural), so a malformed block is an error
			// regardless of whether the command would run.
			var prefix, body, suffix []string
			var closed bool
			if strings.HasPrefix(lines[i], "```") {
				prefix, body, suffix, closed, i = scanFenced(lines, i)
				if !closed {
					return res, fmt.Errorf("line %d: wit: %q: fenced block never closed (missing ```)", d.line+1, d.command)
				}
			} else {
				var nested int
				prefix, body, suffix, closed, nested, i = scanRegion(lines, i)
				if nested >= 0 {
					return res, fmt.Errorf("line %d: wit: directive nested inside the region opened at line %d (regions do not nest)", nested+1, d.line+1)
				}
				if !closed {
					return res, fmt.Errorf("line %d: wit: %q: region never closed (missing %s)", d.line+1, d.command, witEnd)
				}
			}
			want, runErr := run(d.command, dir)
			if runErr != nil {
				// UNAVAILABLE: keep the cached body exactly as-is, never overwrite
				// evidence with an outage. The whole-file write is aborted below.
				res.unavail = append(res.unavail, d.command)
				fmt.Printf("  UNAVAILABLE: %s\n    %v\n", d.command, runErr)
				out = append(out, prefix...)
				out = append(out, body...)
				out = append(out, suffix...)
				continue
			}
			if strings.Join(body, "\n") != want {
				if mode == "generate" {
					body = strings.Split(want, "\n")
					res.changed++
				} else {
					res.drift++
					fmt.Printf("  STALE: %s\n", d.command)
				}
			}
			out = append(out, prefix...)
			out = append(out, body...)
			out = append(out, suffix...)

		case "get":
			// A whole-line wit:get with no inline value is malformed: a get must
			// carry its value between markers (handled above when inline).
			return res, fmt.Errorf("line %d: wit:get %q must wrap a visible value: <!-- wit:get %s -->VALUE<!-- wit:end %s -->", i+1, d.name, d.name, d.name)
		}
	}

	// Write only when a witness body actually changed. This is the no-op rule:
	// wit must never rewrite bytes (e.g. canonicalize CRLF to LF) while reporting
	// "up to date". CRLF normalization happens only as a side effect of a real
	// change, never on its own. All-or-nothing: any unavailable witness aborts the
	// write so the file stays byte-for-byte as the last good evidence.
	if mode == "generate" && len(res.unavail) == 0 && res.changed > 0 {
		if err := writeFileAtomic(file, strings.Join(out, "\n")); err != nil {
			return res, err
		}
	}
	return res, nil
}

// parseDirective parses a strict whole-line wit directive (wit:, wit:set,
// wit:get). It returns ok=false for anything that is not an exact valid form;
// process rejects a malformed standalone wit comment before it can pass through
// as prose, so "ok=false" does not mean "silently ignored".
// looksLikeWitDirective reports whether an HTML comment body has the SHAPE of a
// wit directive: it begins the directive namespace ("wit:" or "wit "), or is
// exactly "wit:end". Ordinary comments that merely start with the letters "wit"
// (with, witness, witfoo) and the bare "wit" are not directive-shaped and stay
// inert prose; only directive shapes are policed.
func looksLikeWitDirective(body string) bool {
	return body == "wit:end" ||
		strings.HasPrefix(body, "wit:") ||
		strings.HasPrefix(body, "wit ")
}

func parseDirective(line string, idx int) (directive, bool) {
	body, ok := htmlComment(line)
	if !ok || !strings.HasPrefix(body, "wit:") {
		return directive{}, false
	}
	rest := strings.TrimPrefix(body, "wit:")

	switch {
	case strings.HasPrefix(rest, "set "):
		name, cmd, ok := splitNameCommand(strings.TrimPrefix(rest, "set "))
		if !ok {
			return directive{}, false
		}
		return directive{kind: "set", name: name, command: cmd, line: idx}, true

	case strings.HasPrefix(rest, "get "):
		name := strings.TrimSpace(strings.TrimPrefix(rest, "get "))
		if !validName(name) {
			return directive{}, false
		}
		return directive{kind: "get", name: name, line: idx}, true

	case strings.HasPrefix(rest, " "):
		// "wit: COMMAND"
		cmd := strings.TrimSpace(rest)
		if cmd == "" {
			return directive{}, false
		}
		return directive{kind: "cmd", command: cmd, line: idx}, true
	}
	return directive{}, false
}

// getOpenPrefix begins an inline wit:get span; the full span is
//
//	<!-- wit:get NAME -->VALUE<!-- wit:end NAME -->
//
// A span is recognized only when a complete open/close pair with a valid,
// matching name is present. A lone marker (e.g. prose documenting the syntax) is
// left as literal text, not an error, so docs can mention markers freely. A real,
// completed span whose variable is undefined IS an error (the document claims a
// value that cannot be produced).
const getOpenPrefix = "<!-- wit:get "

// handleGetLine rewrites (generate) or verifies (check) every complete wit:get
// span on a line. In generate mode VALUE is replaced with the variable; in check
// mode a mismatch counts as drift. If the variable's wit:set was UNAVAILABLE, the
// span's cached value is preserved as-is (not drift, not rewritten, not an
// undefined-variable error): a variable is evidence too.
func handleGetLine(line string, vars map[string]string, unavailVars map[string]bool, mode string) (out string, changed, drift int, err error) {
	var b strings.Builder
	for {
		open := strings.Index(line, getOpenPrefix)
		if open < 0 {
			b.WriteString(line)
			break
		}
		rest := line[open:]
		openEnd := strings.Index(rest, dirClose)
		if openEnd < 0 {
			// No comment terminator: not a span, emit up to here and move on.
			b.WriteString(line[:open+len(getOpenPrefix)])
			line = line[open+len(getOpenPrefix):]
			continue
		}
		name := strings.TrimSpace(rest[len(getOpenPrefix):openEnd])
		afterOpen := rest[openEnd+len(dirClose):]
		closeMarker := "<!-- wit:end " + name + dirClose
		closeAt := -1
		if validName(name) {
			closeAt = strings.Index(afterOpen, closeMarker)
		}
		if closeAt < 0 {
			// Not a complete, well-named span: treat the open marker as literal.
			b.WriteString(line[:open+openEnd+len(dirClose)])
			line = afterOpen
			continue
		}

		// The variable's source was unavailable: preserve the cached value exactly.
		// The UNAVAILABLE is already reported via the failed wit:set command.
		if unavailVars[name] {
			b.WriteString(line[:open])
			b.WriteString(getOpenPrefix + name + dirClose)
			b.WriteString(afterOpen[:closeAt])
			b.WriteString(closeMarker)
			line = afterOpen[closeAt+len(closeMarker):]
			continue
		}

		val, ok := vars[name]
		if !ok {
			return "", 0, 0, fmt.Errorf("wit:get %q: variable not set (a wit:set must precede it)", name)
		}
		current := afterOpen[:closeAt]
		if current != val {
			if mode == "generate" {
				changed++
			} else {
				drift++
			}
		}
		shown := current
		if mode == "generate" {
			shown = val
		}
		b.WriteString(line[:open])
		b.WriteString(getOpenPrefix + name + dirClose)
		b.WriteString(shown)
		b.WriteString(closeMarker)
		line = afterOpen[closeAt+len(closeMarker):]
	}
	return b.String(), changed, drift, nil
}

// htmlComment returns the inner text of a line that is exactly one HTML comment
// "<!-- BODY -->", trimmed. ok is false for any other line.
func htmlComment(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, dirOpen) || !strings.HasSuffix(s, dirClose) {
		return "", false
	}
	inner := s[len(dirOpen) : len(s)-len(dirClose)]
	// Reject a line that contains a second "-->" (e.g. a get span on its own
	// line): those are handled by handleGetLine, not here.
	if strings.Contains(inner, dirClose) {
		return "", false
	}
	return inner, true
}

// splitNameCommand splits "NAME COMMAND" into a valid variable name and the rest.
func splitNameCommand(s string) (name, command string, ok bool) {
	s = strings.TrimLeft(s, " ")
	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return "", "", false
	}
	name = s[:sp]
	command = strings.TrimSpace(s[sp+1:])
	if !validName(name) || command == "" {
		return "", "", false
	}
	return name, command, true
}

// validName reports whether s matches [A-Za-z_][A-Za-z0-9_]*.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// scanFenced consumes a ```...``` block starting at lines[i]. It returns the
// prefix lines (the opening fence), the body lines, the suffix lines (closing
// fence), whether a closing fence was found, and the index after the block.
// closed is false when the block runs to end-of-file with no closing ```; the
// caller treats that as an error rather than rewriting to EOF.
func scanFenced(lines []string, i int) (prefix, body, suffix []string, closed bool, next int) {
	open := fenceLen(lines[i])
	prefix = []string{lines[i]} // opening fence
	i++
	start := i
	// Close only on a fence at least as long as the opener, so a target whose
	// body contains a shorter fence is not truncated early.
	for i < len(lines) && fenceLen(lines[i]) < open {
		i++
	}
	body = lines[start:i]
	if i < len(lines) {
		suffix = []string{lines[i]} // closing fence
		i++
		closed = true
	}
	return prefix, body, suffix, closed, i
}

// fenceLen returns the number of leading backticks if line is a code fence
// (three or more backticks, optionally followed by an info string), else 0.
// CommonMark also allows tilde fences; those are out of scope (documented).
func fenceLen(line string) int {
	n := 0
	for n < len(line) && line[n] == '`' {
		n++
	}
	if n < 3 {
		return 0
	}
	return n
}

// scanRegion consumes lines from i up to a bare "<!-- wit:end -->" line. The body
// is the content between; the suffix is the end marker. closed is false when no
// end marker is found before end-of-file. nested is the line index of a wit:
// command/set directive found inside the region (regions do not nest), or -1.
// The caller treats either condition as an error.
func scanRegion(lines []string, i int) (prefix, body, suffix []string, closed bool, nested, next int) {
	start := i
	nested = -1
	for i < len(lines) && strings.TrimSpace(lines[i]) != witEnd {
		if nested < 0 {
			if d, ok := parseDirective(lines[i], i); ok && (d.kind == "cmd" || d.kind == "set") {
				nested = i
			}
		}
		i++
	}
	body = lines[start:i]
	if i < len(lines) {
		suffix = []string{lines[i]} // end marker
		i++
		closed = true
	}
	return nil, body, suffix, closed, nested, i
}

// run executes cmd via the shell in dir and returns its stdout with one trailing
// newline trimmed (markdown blocks do not carry the command's final newline). The
// command is bounded by cmdTimeout; exceeding it is an error, not a hang.
func run(cmd, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err := c.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timed out after %s", cmdTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSuffix(stdout.String(), "\n"), nil
}

// writeFileAtomic writes content to a temp file in the same directory and renames
// it over file, so a crash mid-write cannot leave a half-written document.
func writeFileAtomic(file, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".wit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, file)
}
