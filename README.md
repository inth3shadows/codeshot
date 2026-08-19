# Codeshot

**A picture of what calls what.** Renders a symbol's [CodeGraph](https://github.com/colbymchenry/codegraph) call trail (who calls it, what it calls) — or a whole repo's file-level dependency graph — as an image.

---

## What it does

```bash
codeshot RollAutoSnapshot --path ~/code/myrepo --out callgraph.png
```

Pulls the symbol's callers/callees straight from CodeGraph's index (`codegraph callers`/`callees --json`) and renders them through [graphviz](https://graphviz.org/) — a real diagram, not hand-drawn ASCII. Test callers are shown dashed so production call paths stand out; a module-level/import reference CodeGraph couldn't resolve to a real call site is shown dotted-gray instead of looking like a confirmed call.

## Why this exists

`TECHNICAL.md`-style docs usually stop at a whole-system, hand-drawn diagram — accurate for the big picture, but nobody hand-draws a diagram for "what exactly touches this one function." Codeshot fills that gap: point it at a symbol, get back a picture, generated from the live index instead of remembered or redrawn by hand.

## Install

```bash
npm install -g github:inth3shadows/codeshot
```

**Requirements:**
- Node.js ≥ 18
- [`codegraph`](https://github.com/colbymchenry/codegraph) CLI on PATH, **1.5.0 or later**, with the target repo indexed (`codegraph init`). `--architecture` mode needs 1.5.0+ specifically: earlier versions have a call-resolution bug (fixed by codegraph's `LITERAL_RECEIVER_TYPES` fix) that can silently fabricate cross-file edges from unrelated builtin method calls (e.g. `/regex/.test(x)`) whose name happens to collide with a real project symbol.
- `graphviz` (`dot`) on PATH — `brew install graphviz` / `apt install graphviz`

Codeshot checks for both on startup and tells you exactly what's missing and how to install it.

## Usage

```bash
codeshot <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--format <fmt>] [--depth <n>]
```

- `--path` — repo to query (defaults to cwd)
- `--out` — output file (defaults to a temp file named after the chosen `--format`; path is printed on success). If the extension doesn't match `--format` (e.g. `--out diagram.svg` without `--format svg`), Codeshot warns on stderr instead of silently writing the wrong data under that name.
- `--limit` — max callers/callees to fetch (defaults to 50; must be a positive integer). Codeshot warns on stderr if a result may be truncated — see [TECHNICAL.md](TECHNICAL.md#configuration) for why and its one known false-positive case.
- `--max-render` — cap how many distinct nodes are drawn in the image, independent of `--limit` (unset by default: no cap). This is one shared budget across callers, callees, and `--depth`'s transitive edges combined — not a separate `N` for each. Useful for symbols with hundreds of callers, where a high `--limit` keeps the truncation warning accurate but would otherwise produce an unreadably tall image.
- `--format` — output format, passed straight to `dot -T<fmt>` (defaults to `png`). `svg` is a good alternative for large graphs — it stays crisp at any zoom level and keeps text selectable, unlike a raster PNG. In `svg` (and `svgz`) output, each node also carries the file its symbol lives in as a hover tooltip, so you can tell same-named symbols apart without cluttering the boxes (open the file in a browser to see them; GitHub's SVG sanitizer may strip tooltips when the image is embedded in a README). Any format `dot -T` supports works; an unsupported one fails with `dot`'s own error listing the valid ones.
- `--embed <file.md>` — instead of just writing an image, insert (or, on re-runs, refresh in place) the diagram inside an existing markdown doc, using idempotent `<!-- codeshot:<id>:start/end -->` markers (the `doctoc`/`terraform-docs` pattern). The image is written to a stable path next to the doc so the relative link resolves and both can be committed. Refreshes an existing doc — it won't create one. Works in both symbol and `--architecture` mode. See [USAGE.md](USAGE.md#embedding-a-diagram-in-your-docs-and-keeping-it-fresh).
- `--check` — (only with `--embed`) verify the committed diagram and its doc block are current without changing anything: exit `0` if up to date, exit `1` if the code has drifted from the committed image. Built for a CI job / pre-commit hook so a stale diagram fails the build. Compares rendered bytes, so it needs the same `graphviz` version that generated the committed image.
- `--depth` — how many hops of callers-of-callers / callees-of-callees to draw beyond the direct trail (defaults to `1`, i.e. today's direct-only behavior; must be a positive integer). Codeshot fetches this itself, one sequential `codegraph` call per newly discovered node — CodeGraph has no multi-hop traversal of its own for `callers`/`callees`. Each additional hop is drawn in a progressively lighter shade so you can tell how far a node is from the symbol at a glance. There's a safety cap on total nodes discovered (a well-connected symbol at `--depth 3`+ can otherwise mean hundreds of sequential `codegraph` calls); Codeshot warns on stderr if it hit that cap before finishing — see `--max-depth-nodes` below to raise it, or [TECHNICAL.md](TECHNICAL.md#configuration) for the default and rationale.
- `--max-depth-nodes` — raises (or lowers) `--depth`'s safety cap on total discovered nodes (defaults to `200`; must be a positive integer). Only applies with `--depth > 1`; has no effect with `--architecture` and is rejected if passed alongside it. Useful for a genuinely well-connected symbol whose graph is real but incomplete at the default cap — see the depth-budget warning it's meant to answer.

## Whole-repo architecture diagram

```bash
codeshot --architecture --path ~/code/myrepo --out architecture.svg --format svg
# ...or, on a repo with more files than fit in one readable picture:
codeshot --architecture --path ~/code/myrepo --group-depth 1 --out modules.svg --format svg
```

A second mode, distinct from the single-symbol trail above: instead of one
symbol's callers/callees, it enumerates every symbol in the repo's CodeGraph
index and probes each one's callees, then aggregates the results into a
**file-to-file** dependency graph (edge label = number of calls between that
pair of files). Self-file edges (a function calling another function in the
same file) are dropped — this is about cross-file coupling, not intra-file
structure. Test files render dashed, same visual language as symbol mode.

This is a real, data-derived graph, not a hand-drawn architecture diagram —
it won't look like a curated conceptual pipeline diagram, it'll look like
what the code actually calls into. On a repo of any real size this is a
slow operation (one sequential `codegraph` call per enumerated symbol), so
two extra flags exist specifically for this mode:
- `--max-symbols` — cap how many symbols get probed (default 500). Codeshot
  warns on stderr if this cuts the scan short.
- `--group-depth <n>` — roll files up into their first `n` directory segments
  and draw *those* as the nodes (`--group-depth 1` on `src/api/user.js` →
  `src/`), summing the call weights of every file pair that collapses into the
  same pair of groups. This is the readable view of a repo big enough that the
  per-file graph is a hairball. Unlike `--max-render`, which drops the
  least-busy *files* outright — taking every edge that touched them with it —
  grouping keeps every **cross-module** call and just draws it at module
  resolution. Calls that become intra-group are dropped, for the same reason
  same-file calls already are: this diagram is about coupling between modules,
  not inside them. So the summed weights on a grouped diagram are legitimately
  lower than the per-file one's, often much lower — that's the intra-module
  traffic, not a lost edge. Repo-root files (no directory to roll into) stay
  themselves.
- `--depth` has no effect here and is rejected if passed — there's no
  multi-hop file-traversal concept to apply it to.

`--limit` and `--max-render` are reused with the same meaning as symbol
mode (callees fetched per probed symbol; distinct nodes actually drawn,
here ranked by busiest file rather than caller/callee priority).

See [TECHNICAL.md](TECHNICAL.md#architecture) for the known limitation
around same-named symbols across files, and why the graph can be slow on
larger repos.

## Diff-scoped diagram

```bash
codeshot --diff --path ~/code/myrepo --out changed.svg --format svg
# ...or against a specific range/ref, for reproducibility in CI:
codeshot --diff --diff-ref origin/main...HEAD --path ~/code/myrepo --out pr.svg --format svg
```

A third mode: instead of one named symbol or the whole repo, it diagrams
just the symbols defined in **changed files** — every symbol codegraph's
index attributes to a file `git diff` reports as touched — bolded as the
diagram's roots, with their direct callers/callees fanned out around them
in the usual house style. Built on the same per-symbol `callers`/`callees`
fetch as single-symbol mode, just run once per changed symbol instead of
once for the one you named.

- No value after `--diff` diffs the **working tree against HEAD** (both
  staged and unstaged changes — "what have I changed right now"; note this
  is `git diff HEAD`, not bare `git diff`, which only covers unstaged edits
  and would miss anything already `git add`ed). `--diff-ref <range>` diffs
  an explicit ref or range instead (e.g. `origin/main...HEAD`) — use this
  form for `--embed --check` in CI, where there's no working tree to diff
  and the default would make the check flap for reasons unrelated to the code.
- `--limit`, `--max-render`, and `--max-symbols` are reused with the same
  meaning as symbol/architecture mode: `--limit` still bounds each changed
  symbol's callers/callees fetch, `--max-symbols` now caps how many *changed*
  symbols get probed (not the whole repo), and `--max-render` bounds only the
  callers/callees pulled in around the changed symbols — every changed symbol
  itself is always drawn, since it's the reason the diagram exists.
- `--depth` and `--max-depth-nodes` have no effect here and are rejected if
  passed — no multi-hop traversal in this mode.
- Composes with `--embed`/`--check` the same as the other two modes — a
  changed-files diagram in a PR description, kept fresh and CI-guarded.
  `--diff-ref` gets its own marker id and default filename (`codeshot:diff-<ref>`,
  same pattern `--group-depth` uses for `--architecture`), so a release-range
  diagram and a PR-range diagram can live in the same doc without one
  silently overwriting the other; a bare `--diff` keeps the plain `codeshot:diff` id.
- Same same-named-symbol caveat as `--architecture` (see TECHNICAL.md):
  codegraph's `callers`/`callees` take a bare name, so two changed symbols
  sharing a name in different files can be ambiguous; codeshot warns when
  this applies rather than silently misattributing an edge.

## Design decisions

**Why shell out to the CodeGraph CLI instead of reading its SQLite index directly?**
The CLI's `--json` output is a stable, documented contract; the on-disk schema isn't. Slower, but survives CodeGraph upgrades.

**Why graphviz instead of a JS graph-drawing library?**
Zero new npm dependencies, and `dot` already produces clean, deterministic layouts — no layout algorithm to hand-tune.

**Standalone tool, not a CodeGraph PR — for now.** A native `codegraph render` would be strictly better (no shell-out, ships free via MCP). This stays a separate tool until it's proven useful across real repos; premature upstreaming risks getting redesigned in review before the idea is validated.

## Status

Early — actively used and maintained on real repos. Previous design (a terminal protocol-handler for staging Claude Code's suggested commands) was retired; see git history if you're curious what that looked like.

**Ran the old installer?** If you installed the retired design (`install/install.ps1` or `install/install.sh`, no longer in this repo), run [`install/uninstall-legacy.ps1`](install/uninstall-legacy.ps1) (Windows) or [`install/uninstall-legacy.sh`](install/uninstall-legacy.sh) (macOS) once to remove the leftover protocol handler registration, Claude Code Stop hook entry, and `~/.codeshot` directory.

## Related Documentation

- [Technical Reference](TECHNICAL.md) — architecture, file descriptions, configuration, maintenance
- [Usage Guide](USAGE.md) — day-to-day usage and troubleshooting
