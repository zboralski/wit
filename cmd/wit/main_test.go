package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The README's documented install path must match the actual module path, so a
// future module rename that forgets the README fails the build rather than
// shipping a false `go install` line.
func TestReadmeInstallPathMatchesModule(t *testing.T) {
	gomod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var module string
	for _, line := range strings.Split(string(gomod), "\n") {
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	if module == "" {
		t.Fatal("no module line in go.mod")
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "go install " + module + "/cmd/wit@latest"
	if !strings.Contains(string(readme), want) {
		t.Errorf("README missing install line %q (module is %s)", want, module)
	}
}

// The usage text lists both subcommands; the README witnesses it via
// `wit help`, so it must mention generate and check.
func TestUsageListsBothCommands(t *testing.T) {
	for _, want := range []string{"wit generate", "wit check", "check|generate"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing %q:\n%s", want, usage)
		}
	}
}

// 1-3. A fenced wit: block: write fills it, check passes when fresh, check fails
// on drift.
func TestFencedBlock(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: echo hello -->\n```\nstale\n```\n")

	if res, err := process(md, "check"); err != nil || res.drift != 1 {
		t.Fatalf("check stale: drift=%d err=%v, want 1", res.drift, err)
	}
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("write: changed=%d err=%v, want 1", res.changed, err)
	}
	if got := read(t, md); got != "<!-- wit: echo hello -->\n```\nhello\n```\n" {
		t.Fatalf("after write = %q", got)
	}
	if res, err := process(md, "check"); err != nil || res.drift != 0 {
		t.Fatalf("check after write: drift=%d err=%v, want 0", res.drift, err)
	}
	if res, _ := process(md, "generate"); res.changed != 0 {
		t.Fatalf("second write changed=%d, want 0 (idempotent)", res.changed)
	}
}

// 4-6. A markdown region (bare wit:end): write fills it, check passes when fresh,
// check fails on drift. The region renders (not a code block).
func TestRegionBlock(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "before\n<!-- wit: printf 'a\\nb' -->\nwrong\n<!-- wit:end -->\nafter\n")

	if res, err := process(md, "check"); err != nil || res.drift != 1 {
		t.Fatalf("region check stale: drift=%d err=%v, want 1", res.drift, err)
	}
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("region write: changed=%d err=%v", res.changed, err)
	}
	want := "before\n<!-- wit: printf 'a\\nb' -->\na\nb\n<!-- wit:end -->\nafter\n"
	if got := read(t, md); got != want {
		t.Fatalf("region after write = %q, want %q", got, want)
	}
	if res, _ := process(md, "check"); res.drift != 0 {
		t.Fatalf("region check after write: drift=%d, want 0", res.drift)
	}
}

// Invariant: wit: says how to regenerate; the target says how it renders. A
// fenced target preserves its fence language (the directive never carries a type).
func TestFenceLanguagePreserved(t *testing.T) {
	for _, lang := range []string{"go", "json", "text", ""} {
		dir := t.TempDir()
		md := filepath.Join(dir, "doc.md")
		write(t, md, "<!-- wit: printf X -->\n```"+lang+"\nstale\n```\n")
		if _, err := process(md, "generate"); err != nil {
			t.Fatalf("lang %q: generate: %v", lang, err)
		}
		want := "<!-- wit: printf X -->\n```" + lang + "\nX\n```\n"
		if got := read(t, md); got != want {
			t.Errorf("lang %q: fence not preserved\n got %q\nwant %q", lang, got, want)
		}
	}
}

// Invariant: a region's generated Markdown owns any nested code fences and their
// language tags; the region is closed only by a bare wit:end, not an inner fence.
func TestRegionOwnsNestedFences(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: printf '## R\\n```json\\n{\"ok\":true}\\n```' -->\nstale\n<!-- wit:end -->\n")
	if _, err := process(md, "generate"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	want := "<!-- wit: printf '## R\\n```json\\n{\"ok\":true}\\n```' -->\n## R\n```json\n{\"ok\":true}\n```\n<!-- wit:end -->\n"
	if got := read(t, md); got != want {
		t.Fatalf("nested-fence region\n got %q\nwant %q", got, want)
	}
	if res, err := process(md, "check"); err != nil || res.drift != 0 {
		t.Fatalf("nested-fence region not idempotent: drift=%d err=%v", res.drift, err)
	}
}

// A wit comment is executable evidence: a standalone HTML comment whose body
// begins with "wit" but is not a valid directive must fail loudly, not pass
// through as prose. Prose that merely mentions a marker inline stays literal.
func TestMalformedDirectiveFails(t *testing.T) {
	bad := map[string]string{
		"unknown verb":    "<!-- wit:frobnicate x -->\n",
		"set missing cmd": "<!-- wit:set bad -->\n",
		"set bad name":    "<!-- wit:set 1bad echo hi -->\n",
		"get no name":     "<!-- wit:get -->\n",
		"get bad name":    "<!-- wit:get bad name -->\n",
		"empty wit":       "<!-- wit: -->\n",
		"bare wit space":  "<!-- wit bogus -->\n",
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			md := filepath.Join(dir, "doc.md")
			write(t, md, content)
			if _, err := process(md, "check"); err == nil {
				t.Errorf("check: expected error for %q", content)
			}
			if _, err := process(md, "generate"); err == nil {
				t.Errorf("generate: expected error for %q", content)
			}
			if got := read(t, md); got != content {
				t.Errorf("generate altered the file on error: got %q", got)
			}
		})
	}

	// Inline prose mentioning markers must NOT trip the strict check: these lines
	// are not standalone wit comments.
	ok := map[string]string{
		"prose get marker": "See `<!-- wit:get foo -->` in your doc.\n",
		"prose end marker": "Close it with <!-- wit:end --> on its own line.\n",
	}
	for name, content := range ok {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			md := filepath.Join(dir, "doc.md")
			write(t, md, content)
			if _, err := process(md, "check"); err != nil {
				t.Errorf("check: prose %q should be inert, got %v", content, err)
			}
		})
	}
}

// Ordinary HTML comments that merely begin with the letters "wit" are NOT
// directive-shaped (no wit:, no "wit ", not exactly wit/wit:end) and stay inert.
func TestNonWitCommentsAreInert(t *testing.T) {
	// Includes bare "<!-- wit -->": it is not a directive shape (no use), so it is
	// inert prose, not a reserved/malformed directive.
	content := "<!-- with caveat -->\n<!-- witness note -->\n<!-- witfoo -->\n<!-- wit -->\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	if _, err := process(md, "check"); err != nil {
		t.Fatalf("non-directive comments should be inert: %v", err)
	}
	if got := read(t, md); got != content {
		t.Errorf("inert comments altered: %q", got)
	}
}

// A duplicate wit:set must fail even when the first set's command was unavailable:
// the variable already exists, so redefining it is an error, not a second attempt.
func TestDuplicateSetAfterUnavailableSetFails(t *testing.T) {
	content := "<!-- wit:set n false -->\n<!-- wit:set n echo 1 -->\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected duplicate wit:set even when the first set is unavailable")
	}
}

// A four-backtick demo fence must span its inner three-backtick example, so a
// live-looking directive inside it is inert. The inner directive would fail
// (exit 99) if it ran, so a clean check/generate proves it did not.
func TestFourBacktickDemoFenceIsInert(t *testing.T) {
	content := "````md\n<!-- wit: exit 99 -->\n```text\nx\n```\n````\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	if _, err := process(md, "check"); err != nil {
		t.Errorf("check ran the demo directive: %v", err)
	}
	if res, err := process(md, "generate"); err != nil || res.changed != 0 {
		t.Errorf("generate touched the demo fence: changed=%d err=%v", res.changed, err)
	}
	if got := read(t, md); got != content {
		t.Errorf("demo fence altered: got %q", got)
	}
}

// A wit: target whose own fenced body contains a shorter fence is closed by the
// matching-length fence, not the inner one (fence-length rule).
func TestTargetFenceWithShorterInnerFence(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	// printf emits a body that itself contains a ``` line; the target uses a
	// four-backtick fence so that inner ``` does not close it.
	write(t, md, "<!-- wit: printf 'a\\n```\\nb' -->\n````text\nstale\n````\n")
	if _, err := process(md, "generate"); err != nil {
		t.Fatalf("generate: %v", err)
	}
	want := "<!-- wit: printf 'a\\n```\\nb' -->\n````text\na\n```\nb\n````\n"
	if got := read(t, md); got != want {
		t.Fatalf("inner-fence target\n got %q\nwant %q", got, want)
	}
	if res, err := process(md, "check"); err != nil || res.drift != 0 {
		t.Fatalf("not idempotent: drift=%d err=%v", res.drift, err)
	}
}

// Empty command output is defined behavior: the target body is a single blank
// line, and generate is idempotent (no perpetual drift).
func TestEmptyOutput(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: printf '' -->\n```text\nold\n```\n")
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("generate: changed=%d err=%v, want 1", res.changed, err)
	}
	want := "<!-- wit: printf '' -->\n```text\n\n```\n"
	if got := read(t, md); got != want {
		t.Fatalf("empty output = %q, want one blank body line %q", got, want)
	}
	if res, err := process(md, "check"); err != nil || res.drift != 0 {
		t.Fatalf("empty output not idempotent: drift=%d err=%v", res.drift, err)
	}
}

// A failing command is UNAVAILABLE, not STALE and not a structural error: in both
// modes the cached witnessed text is preserved and the file is unchanged.
func TestUnavailablePreservesEvidence(t *testing.T) {
	content := "<!-- wit: false -->\n```text\ncached evidence\n```\n"
	for _, mode := range []string{"check", "generate"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			md := filepath.Join(dir, "doc.md")
			write(t, md, content)
			res, err := process(md, mode)
			if err != nil {
				t.Fatalf("%s: unexpected structural error: %v", mode, err)
			}
			if len(res.unavail) != 1 {
				t.Errorf("%s: unavail=%d, want 1", mode, len(res.unavail))
			}
			if res.drift != 0 {
				t.Errorf("%s: a failed command must not count as drift (got %d)", mode, res.drift)
			}
			if got := read(t, md); got != content {
				t.Errorf("%s: cached evidence not preserved: got %q", mode, got)
			}
		})
	}
}

// The exact variable case from review: an unavailable wit:set must not poison its
// dependent wit:get. The cached visible value (12) is preserved, the variable is
// UNAVAILABLE (not undefined), and the file is unchanged.
func TestUnavailableSetPreservesCachedGet(t *testing.T) {
	content := "<!-- wit:set n missing-tool -->\ncount: <!-- wit:get n -->12<!-- wit:end n -->\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	res, err := process(md, "generate")
	if err != nil {
		t.Fatalf("structural error: %v", err)
	}
	if len(res.unavail) != 1 {
		t.Fatalf("unavail=%d, want 1", len(res.unavail))
	}
	if got := read(t, md); got != content {
		t.Fatalf("cached get changed: got %q", got)
	}
}

// A command timeout is UNAVAILABLE (evidence could not be produced), not drift.
func TestTimeoutIsUnavailable(t *testing.T) {
	old := cmdTimeout
	cmdTimeout = 50 * time.Millisecond
	defer func() { cmdTimeout = old }()

	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	content := "<!-- wit: sleep 5 -->\n```text\ncached\n```\n"
	write(t, md, content)
	res, err := process(md, "check")
	if err != nil {
		t.Fatalf("structural error: %v", err)
	}
	if len(res.unavail) != 1 {
		t.Errorf("unavail=%d, want 1", len(res.unavail))
	}
	if got := read(t, md); got != content {
		t.Errorf("cached preserved? got %q", got)
	}
}

// All-or-nothing: if a later witness is unavailable, an earlier witness that
// would have changed must NOT be written. The file stays byte-for-byte.
func TestUnavailableAbortsWholeFile(t *testing.T) {
	content := "<!-- wit: echo new -->\n```text\nold\n```\n<!-- wit: false -->\n```text\ncached\n```\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	res, err := process(md, "generate")
	if err != nil {
		t.Fatalf("structural error: %v", err)
	}
	if len(res.unavail) != 1 {
		t.Errorf("unavail=%d, want 1", len(res.unavail))
	}
	if got := read(t, md); got != content {
		t.Errorf("partial write occurred: first block changed.\n got %q\nwant %q", got, content)
	}
	// The first block's BODY must still be "old", not regenerated to "new".
	if !strings.Contains(read(t, md), "```text\nold\n```") {
		t.Error("earlier witness body was updated despite a later unavailable witness")
	}
}

// A structural error (malformed directive) is NOT reported as UNAVAILABLE: it is
// a hard error from process, distinct from a command that could not run.
func TestStructuralErrorNotUnavailable(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit:bogus x -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected a structural error for a malformed directive")
	}
}

// No-op: when every witness already matches, generate writes nothing (reports 0
// changed and leaves the file untouched), so there is no diff churn.
func TestNoOpGenerateDoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: echo hi -->\n```text\nhi\n```\n")
	// First generate makes it match (it already does), so changed must be 0.
	res, err := process(md, "generate")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if res.changed != 0 {
		t.Errorf("changed=%d, want 0 (already up to date)", res.changed)
	}
}

// 7. wit:set followed by an inline wit:get: write fills the visible value.
func TestVariableSetGet(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit:set n echo 42 -->\ncount: <!-- wit:get n -->old<!-- wit:end n -->.\n")

	if res, err := process(md, "check"); err != nil || res.drift != 1 {
		t.Fatalf("get check stale: drift=%d err=%v, want 1", res.drift, err)
	}
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("get write: changed=%d err=%v, want 1", res.changed, err)
	}
	want := "<!-- wit:set n echo 42 -->\ncount: <!-- wit:get n -->42<!-- wit:end n -->.\n"
	if got := read(t, md); got != want {
		t.Fatalf("after write = %q, want %q", got, want)
	}
}

// 8. One wit:set, two wit:get of the same name: command runs once, both render.
func TestVariableRepeatedGet(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit:set c echo X -->\na <!-- wit:get c -->?<!-- wit:end c --> b <!-- wit:get c -->?<!-- wit:end c -->\n")

	if res, err := process(md, "generate"); err != nil || res.changed != 2 {
		t.Fatalf("write: changed=%d err=%v, want 2", res.changed, err)
	}
	want := "<!-- wit:set c echo X -->\na <!-- wit:get c -->X<!-- wit:end c --> b <!-- wit:get c -->X<!-- wit:end c -->\n"
	if got := read(t, md); got != want {
		t.Fatalf("after write = %q, want %q", got, want)
	}
}

// 9. wit:get of an undefined variable is an error.
func TestUndefinedGetFails(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "x: <!-- wit:get missing -->?<!-- wit:end missing -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected error for undefined wit:get")
	}
}

// 10. Duplicate wit:set of the same name is an error.
func TestDuplicateSetFails(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit:set n echo 1 -->\n<!-- wit:set n echo 2 -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected error for duplicate wit:set")
	}
}

// A command that exits nonzero is UNAVAILABLE (cached evidence preserved), not
// silent drift and not a structural error.
func TestCommandUnavailable(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: exit 3 -->\n```\ncached\n```\n")
	// A nonzero-exit command is UNAVAILABLE (cached evidence preserved), not a
	// structural error and not drift.
	res, err := process(md, "check")
	if err != nil {
		t.Fatalf("unexpected structural error: %v", err)
	}
	if len(res.unavail) != 1 || res.drift != 0 {
		t.Fatalf("unavail=%d drift=%d, want 1/0", len(res.unavail), res.drift)
	}
}

// 12. A wit:get before its wit:set is an error (variables resolve top to bottom).
func TestGetBeforeSetFails(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "early: <!-- wit:get n -->?<!-- wit:end n -->\n<!-- wit:set n echo 1 -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected error for wit:get before wit:set")
	}
}

// 13. A wit:get marker inside a wit: command is not expanded: the command text is
// passed to the shell verbatim, variables never expand into commands.
func TestVariableNotExpandedInCommand(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	// The command prints a literal string containing what looks like a get; it
	// must be treated as plain command text, and the fenced body matches stdout.
	write(t, md, "<!-- wit: echo literal -->\n```\nstale\n```\n")
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("write: changed=%d err=%v", res.changed, err)
	}
	if got := read(t, md); got != "<!-- wit: echo literal -->\n```\nliteral\n```\n" {
		t.Fatalf("after write = %q", got)
	}
}

// 14. A missing wit:end (both block forms) is an error and leaves the file
// untouched, never a best-effort rewrite to end-of-file.
func TestUnterminatedFails(t *testing.T) {
	cases := map[string]string{
		"fenced": "<!-- wit: echo hi -->\n```\nold\ntail that must survive\n",
		"region": "<!-- wit: echo hi -->\nold\nmore tail no end\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			md := filepath.Join(dir, "doc.md")
			write(t, md, content)
			if _, err := process(md, "check"); err == nil {
				t.Error("check: expected error for unterminated block")
			}
			if _, err := process(md, "generate"); err == nil {
				t.Error("write: expected error for unterminated block")
			}
			if got := read(t, md); got != content {
				t.Errorf("write corrupted file: got %q, want unchanged", got)
			}
		})
	}
}

// 15. A nested region (a wit: command target opened inside another) is rejected:
// the inner wit: comment is seen before the outer wit:end, which closes the outer
// region early, leaving the inner directive with no target.
func TestNestedRegionFails(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	// Outer region never closes because the only wit:end is consumed as the
	// outer's terminator while an inner wit: command follows with no block.
	write(t, md, "<!-- wit: echo a -->\nbody\n<!-- wit: echo b -->\n<!-- wit:end -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected error for nested/region-confused blocks")
	}
}

// Variables are scalar: a wit:set whose command emits more than one line is an
// error in both modes (a multi-line value cannot round-trip through a single-line
// wit:get span), and generate must not alter the file.
func TestScalarVariableRejectsMultilineOutput(t *testing.T) {
	content := "<!-- wit:set v printf 'a\\nb' -->\nx <!-- wit:get v -->?<!-- wit:end v --> y\n"
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, content)
	if _, err := process(md, "check"); err == nil {
		t.Error("check: expected error for multi-line wit:set value")
	}
	if _, err := process(md, "generate"); err == nil {
		t.Error("generate: expected error for multi-line wit:set value")
	}
	if got := read(t, md); got != content {
		t.Errorf("generate altered the file on error: got %q", got)
	}
}

// wit:set:multiline is not a valid directive in this release: a standalone line
// using it must fail loudly, not parse.
func TestSetMultilineNotADirective(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit:set:multiline v printf 'a\\nb' -->\n")
	if _, err := process(md, "check"); err == nil {
		t.Fatal("expected error: wit:set:multiline is not a valid directive")
	}
}

// Multiple files in one invocation are reported independently.
func TestMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.md")
	stale := filepath.Join(dir, "stale.md")
	write(t, clean, "<!-- wit: echo ok -->\n```\nok\n```\n")
	write(t, stale, "<!-- wit: echo ok -->\n```\nwrong\n```\n")
	if res, _ := process(clean, "check"); res.drift != 0 {
		t.Errorf("clean drift=%d, want 0", res.drift)
	}
	if res, _ := process(stale, "check"); res.drift != 1 {
		t.Errorf("stale drift=%d, want 1", res.drift)
	}
}

// CRLF input normalizes to LF: a Windows-edited fresh block is not falsely stale,
// and write canonicalizes to LF.
func TestCRLFNormalized(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "doc.md")
	write(t, md, "<!-- wit: echo hi -->\r\n```\r\nhi\r\n```\r\n")
	if res, err := process(md, "check"); err != nil || res.drift != 0 {
		t.Fatalf("CRLF check: drift=%d err=%v, want 0", res.drift, err)
	}
	write(t, md, "<!-- wit: echo hi -->\r\n```\r\nstale\r\n```\r\n")
	if res, err := process(md, "generate"); err != nil || res.changed != 1 {
		t.Fatalf("CRLF write: changed=%d err=%v, want 1", res.changed, err)
	}
	if got := read(t, md); got != "<!-- wit: echo hi -->\n```\nhi\n```\n" {
		t.Fatalf("after write = %q, want LF-normalized", got)
	}
}

// A command exceeding the timeout is UNAVAILABLE, not drift (and not a hang).
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
