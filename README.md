# wit

`wit` keeps claims tied to the thing that proves them.

In a malware review, a permission count, string table, hash, decompiled symbol,
certificate domain, or IOC list should not be copied from memory into prose. It
should be regenerated from the sample, code, API, or dataset that produced it. The
same rule applies to ordinary documentation: code, data, output, and explanation
must move together, or the document starts lying.

It also preserves reproduction. A year later you do not have to remember which API
you used or how you gathered the evidence, because the command is still in the
document, for example `curl -s 'https://crt.sh/?q=%25.example.com&output=json'`.

Humans forget, miscopy, and trust stale notes; AI agents do the same faster and
with more confidence. Requiring `wit` leaves an executable witness beside every
generated claim, so `wit check` can rerun the evidence and prove the text still
matches reality. A `wit` directive says how to regenerate a claim, count, table,
code block, or markdown region, and `wit check` fails if the checked-in text has
drifted from that command's output.

`wit generate` runs the witnessed commands and rewrites the blocks they own;
`wit check` reruns them and fails if the checked-in markdown has drifted.

This README is witnessed by `wit` itself: every block below a `<!-- wit: CMD -->`
comment is produced by that command, and `wit check README.md` fails if any has
drifted.

## Install

```sh
go install github.com/zboralski/wit/cmd/wit@latest
```

Or build from a clone:

```sh
git clone https://github.com/zboralski/wit
cd wit
go build -o wit ./cmd/wit
```

## Usage

<!-- wit: go run ./cmd/wit help -->
```text
usage: wit <check|generate> FILE...

  wit generate FILE...   run the witnessed commands, rewrite the blocks they own
  wit check FILE...      run the same commands, fail if a block has drifted
```

## Discipline

`wit` follows the same execution discipline as Go's
[`go generate`](https://go.dev/blog/generate), adapted for markdown:

- It is an **explicit author command**, not part of markdown rendering, `go
  build`, or `go test` (a project may call it from its own CI script).
- It performs **no dependency analysis**: no mtimes, no build graph, no notion
  that `README.md` depends on some source file. It runs the directives present in
  the document, in document order, when you ask.
- A checked-in document must already **contain** the generated output. A reader
  cloning the repo should not need the author's commands or local tooling just to
  read it; the witnessed output is committed, like a generated artifact.

If you change source, you rerun `wit generate`. CI runs `wit check` as a drift gate.
`wit` itself never becomes the build, the test runner, or the dependency tracker.

## Syntax

One directive namespace, all valid HTML comments. A standalone comment beginning
with `wit:` or `wit `, or one that is exactly `wit:end`, is parsed as a directive
and must be valid (a malformed one fails loudly). Ordinary comments that merely
start with the letters `wit` (`with`, `witness`) are left alone. The examples in
this section are shown inside demo fences so they are not executed.

## Output targets

A `wit:` directive says how to regenerate content. The markdown that follows it
says how that content is rendered.

There are two target forms.

### Fenced block target

Use a fenced block when the command output should render as literal text or code.
The fence owns the language tag, and `wit generate` preserves it.

````md
<!-- wit: go test ./... 2>&1 | tail -1 -->
```text
ok ./internal/spec 0.01s
```
````

If the fence says `json`, the output is JSON. If it says `go`, the output is Go.
`wit` does not need a separate typed directive such as `wit:go:` because Markdown
already has a place for that information.

### Markdown region target

Use a region when the command output should render as Markdown.

```md
<!-- wit: ./scripts/render-summary -->
## Findings
- permissions: 17
- network indicators: 4
<!-- wit:end -->
```

A region is not a code block, so it has no language tag. If generated Markdown
needs typed code, the generated Markdown should include its own fenced block.

````md
<!-- wit: ./scripts/render-json-example -->
Here is the current schema:
```json
{"name":"demo","network":true}
```
<!-- wit:end -->
````

The rule is simple:

- `wit:` tells how to regenerate.
- The following target tells how to render.

`wit` does not invent a second type system over Markdown.

## Variables

A variable captures one command's output once and renders it visibly inline, so a
short fact (a count, hash, version, commit id) can appear in prose and stay
checkable.

```md
<!-- wit:set ngo find . -name '*.go' | wc -l | tr -d ' ' -->
This repo has <!-- wit:get ngo -->2<!-- wit:end ngo --> Go files.
```

- `wit:set NAME COMMAND` runs the command once and stores its stdout. Variables
  are scalar: a `set` whose command emits more than one line is an error, because
  an inline `wit:get` span is single-line and a multi-line value cannot round-trip
  through it. (A block form for multi-line values may come later.)
- `wit:get NAME` renders the value between the `wit:get NAME` marker and a
  matching `wit:end NAME` marker, on one line; the value stays visible.
- Variables resolve top to bottom: a `get` before its `set` is an error, as are a
  duplicate `set` and an undefined `get`.
- Variables never expand into commands: `wit:set` produces values, `wit:get`
  consumes them, `wit:` commands never consume generated values.

## Unavailable evidence

The checked-in generated text is also the last cached witness result. If a command
cannot run later because an API is down, the network is unavailable, a sample
moved, or a local tool is missing, `wit` does not erase the old evidence. It
reports the witness as `UNAVAILABLE`, exits nonzero, and leaves the document
unchanged.

This is different from `STALE`. `STALE` means the command ran and produced
different output. `UNAVAILABLE` means the command did not produce evidence, so the
existing text could not be reverified.

For example, a certificate lookup may depend on an external API:

```sh
curl -s 'https://crt.sh/?q=%25.example.com&output=json'
```

If that API is down a year later, the old certificate list remains in the
document. `wit` preserves it and tells you the source could not be checked,
instead of replacing evidence with an outage. `wit generate` is all-or-nothing per
file: if any witness is unavailable, nothing is written, so a document never
becomes half fresh and half stale. Removing retired evidence is a separate,
manual act, not something normal generation does.

## Notes

`wit` is an author tool: commands run with your shell's full privileges (like
`make`), from the markdown file's directory, capturing stdout only. A malformed
directive, an unterminated block, or an undefined variable fails loudly and
leaves the file untouched. `wit generate` writes atomically, and only when the
regenerated bytes actually differ (no no-op rewrites).

The name comes from `wit.`, the standard legal abbreviation for witness: a `wit`
directive embedded in a document is that witness.

## License

MIT. See [LICENSE](LICENSE).
