# Technical Reference: Codeshot

## Architecture

Codeshot is a single-file CLI (`render/callgraph.js`) with no server, no persistent state, and no dependencies beyond two external binaries it shells out to.

```
argv (symbol, --path, --out, --limit, --max-render, --format, --depth)
        |
        v
requireOnPath('codegraph')  --- exits with an install hint if missing
requireOnPath('dot')        --- exits with an install hint if missing
        |
        v
runCodegraph(['callers', symbol, '--path', repoPath, '--limit', limit, '--json'])
runCodegraph(['callees', symbol, '--path', repoPath, '--limit', limit, '--json'])
        |
        v
truncationWarning(...)    --- warns on stderr if a result hit --limit exactly (fetch cap)
renderTruncationNote(...) --- warns on stderr if distinct results exceed --max-render (render cap)
        |
        v
if --depth > 1: collectTransitive('callers', ...) / collectTransitive('callees', ...)
  --- recursively calls codegraph callers/callees on each newly discovered node,
      sequentially, up to --depth hops or a fixed node budget, producing extra
      { from, to, depth } edges; depthBudgetWarning(...) warns on stderr if the
      budget was hit before --depth was satisfied
        |
        v
buildDot(symbol, callers, callees, { maxRender, transitiveEdges })  --- pure function, produces a DOT string
        |
        v
write DOT to a tempfile -> `dot -T<format> <tempfile> -o <outFile>` -> delete tempfile
        |
        v
print outFile path to stdout
```

See [README.md](README.md#design-decisions)'s "Design decisions" section for why this shells out to the CodeGraph CLI instead of reading its SQLite index, and why graphviz instead of a JS graph-drawing library.

**Why is `buildDot` exported from a file that also runs as a CLI?**
`render/callgraph.js` guards its `main()` call with `if (require.main === module)`, so `node render/callgraph.js <symbol>` still runs the CLI, but `require('./render/callgraph.js')` (used by `test/run.js`) gets `{ buildDot, isTestRef }` without executing anything. This keeps the test suite dependency-free — no test framework, no mocking of `execFileSync`.

## File Descriptions

- **`render/callgraph.js`** — the entire tool. Exports `buildDot(symbol, callers, callees, { maxRender, transitiveEdges })` (pure: turns caller/callee arrays into a DOT digraph string, deduplicating entries with the same `name`+`filePath` so repeated JSON rows don't render as duplicate edges, then — if `maxRender` is given — slicing each *deduplicated* list down to the first `maxRender` entries so the image stays a bounded size; `transitiveEdges`, an optional array of `{ from, to, depth }` pairs from `--depth > 1` traversal, is rendered the same way but colored by `depthColor(depth)` instead of the default edge color — omitting it renders exactly as before `--depth` existed), `dedupeNodes(nodes)` (pure: collapses same-`name`+`filePath` entries, used by both `buildDot` and `main` so distinct-count logic has one source of truth), `dedupeEdges(edges)` (pure: the same idea as `dedupeNodes` but keyed on a `from`+`to` pair, used only for `transitiveEdges`), `depthColor(depth)` (pure: maps hop distance ≥2 to a progressively lighter shade, clamped at the palette's last entry for very deep hops), `isTestRef(node)` (true if a node's name or filePath looks test-related, used to render those edges dashed — applies to `transitiveEdges` too, checked against each edge's `from` node), `truncationWarning(kind, results, limit)` (pure: returns a warning string if `results.length` hit `limit` exactly, else `null` — the *fetch* cap), `renderTruncationNote(kind, distinctCount, maxRender)` (pure: returns a warning string if the deduplicated count exceeds `maxRender`, else `null` — the *render* cap), and `depthBudgetWarning(truncated, budget)` (pure: returns a warning string if `--depth` traversal hit the internal node budget before finishing, else `null`). Everything else (`requireOnPath`, `runCodegraph`, `collectTransitive`, `main`) is CLI plumbing, not exported — `collectTransitive` in particular does real I/O (recursive `codegraph` calls), so like `runCodegraph` it's only exercised by the CLI-level tests, not unit-tested directly.
- **`test/run.js`** — assertion-based test suite (Node's built-in `assert`, no framework) covering `buildDot`, `isTestRef`, `truncationWarning`, `dedupeNodes`, `renderTruncationNote`, `dedupeEdges`, `depthColor`, and `depthBudgetWarning` directly. Run via `npm test`.
- **`package.json`** — declares the `codeshot` bin pointing at `render/callgraph.js`, and the `test` script.
- **`.runechoguardignore`** — false-positive suppression list for the RunEcho pre-commit symbol-resolution guard (a local hook, not part of codeshot itself). Bare-call identifiers the guard can't resolve (e.g. Node builtins passed as function parameters) get listed here instead of disabling the guard.

## External Dependencies

- **[`codegraph`](https://github.com/colbymchenry/codegraph) CLI** — must be on `PATH`; the target repo must already be indexed (`codegraph init`). The `callers` and `callees` queries are run sequentially, not concurrently — running them in parallel intermittently triggers a `UNIQUE constraint failed: schema_versions.version` error from `codegraph` itself, so concurrent invocations against the same index aren't safe.
- **`dot` (Graphviz)** — must be on `PATH`.

Both are checked at startup via `which`/`where` (see the pipeline diagram above); a missing binary prints an install hint and exits 1 rather than failing deep in the call stack.

## Configuration

No environment variables, no config file. All behavior is controlled by CLI arguments:

| Flag | Default | Purpose |
|---|---|---|
| `<symbol>` (positional, required) | — | The symbol to graph |
| `--path` | `.` (cwd) | Repo path passed through to `codegraph`. An explicit empty value (`--path=`) is rejected with an error rather than silently falling through to `codegraph` as an empty string. |
| `--out` | `<tmpdir>/callgraph-<symbol>-<timestamp>.<format>` | Output file path |
| `--limit` | `50` | Max callers/callees fetched from `codegraph` (its own CLI default is 20). Must be a positive integer — rejected with an error otherwise, since `codegraph` silently returns an empty result for a malformed limit rather than erroring itself. `codegraph`'s JSON has no total/truncated field, so Codeshot's only signal that more may exist is the result count hitting `--limit` exactly — when it does, a warning is printed to stderr. That heuristic false-positives for a symbol with exactly `--limit` real results and no more; there's no way to distinguish "exactly complete" from "truncated" without a total field from `codegraph`. |
| `--max-render` | unset (no cap) | Caps how many *distinct* (post-dedup) callers/callees are actually drawn as nodes, independent of `--limit`. Exists because `--limit` controls what's fetched, not what's legible — a symbol with hundreds of real callers is still complete but produces an unusably tall image at a high `--limit`. Opt-in and orthogonal to `--limit`: fetch wide (to get an accurate truncation signal) while rendering narrow (to keep the image readable). Must be a positive integer if given. When it truncates, a stderr warning states how many of the distinct total were rendered. |
| `--format` | `png` | Passed straight to `dot -T<format>` — no allowlist of its own, so any format `dot -T` supports works, and an unsupported one surfaces `dot`'s own error (which lists the valid ones) rather than a Codeshot-invented one. `svg` is the notable alternative: unlike a raster PNG it stays crisp at any zoom and keeps text selectable, which helps more than `--max-render` alone when a graph is dense but you still want to inspect all of it. |
| `--depth` | `1` (today's direct-only behavior) | How many hops of callers-of-callers / callees-of-callees to draw beyond the direct trail. Must be a positive integer. `codegraph`'s own `callers`/`callees` have no traversal depth of their own, so Codeshot implements this client-side: `collectTransitive` recursively calls `callers`/`callees` on each newly discovered node, sequentially (same concurrency-safety reason as the depth-1 calls — see External Dependencies), up to `--depth` hops or `NODE_BUDGET` (200, a fixed constant, not itself configurable) total discovered nodes, whichever comes first. Fan-out is multiplicative with depth and branching factor, so the budget exists specifically to stop a well-connected symbol at `--depth 3`+ from turning into hundreds of sequential `codegraph` calls; if the budget is hit first, `depthBudgetWarning` prints a stderr note that the graph beyond that point is incomplete. Each hop beyond the first is drawn in a progressively lighter edge color (`depthColor`) so distance from the symbol is visible at a glance; `--limit` and `--max-render` are NOT applied per-hop, only globally to the depth-1 fetch/render as before. |

## Maintenance Commands

```bash
npm test              # runs test/run.js — assertions against buildDot/isTestRef/truncationWarning/dedupeNodes/renderTruncationNote/dedupeEdges/depthColor/depthBudgetWarning, plus two CLI-level tests (depth 1, and --depth 2) against this repo's own codegraph index (skips if not codegraph-indexed)
node render/callgraph.js <symbol> --path <repo> --out /tmp/out.png   # manual smoke test
```

There is no service to restart, no rollback beyond `npm uninstall -g codeshot` / reinstalling a prior git ref, and no other services, logs, or scheduled jobs to maintain — see [README.md](README.md#install) for the install command itself.

## Known Limitations

- `test/run.js` includes one CLI-level test that runs the real `codeshot` binary against this repo's own `codegraph` index (querying a symbol from `render/callgraph.js` itself) to catch `codegraph` output-shape drift — but it skips itself (rather than failing) when `codegraph` isn't on `PATH` or this repo hasn't been `codegraph init`'d, since that's a dev-environment convenience a fresh clone or CI won't have. So the contract is only actually exercised on machines set up for it; elsewhere it's still only indirectly covered via `buildDot`'s output against the real `dot` binary.
- `isTestRef` is a naming-convention heuristic (word-boundary `Test`/`Spec` prefix or suffix in the name, or a `test`/`tests`/`spec`/`__tests__` directory or `.test.`/`.spec.` filename), not a semantic check — a production symbol that happens to follow test-like naming (e.g. a function literally named `Test`) would still be misclassified.
- `--depth`'s `NODE_BUDGET` (200) is a fixed internal constant, not exposed as a flag — a genuinely well-connected symbol at `--depth 3`+ in a large repo can still hit it and produce an incomplete graph (with a stderr warning), and there's currently no way to raise the cap short of editing the constant.
- `--depth`'s traversal treats `--limit`/`--max-render` as global, not per-hop — a symbol with a huge fan-out at hop 2 fetches up to `--limit` results for *each* newly discovered node at that hop, which is the main driver of `NODE_BUDGET` exhaustion; there's no independent per-hop limit to trade off against total node count.
- A cyclic call graph (recursion, or A and B calling each other) can cause `--depth`'s transitive traversal to rediscover the root symbol or an already-drawn depth-1 node as a "from"/"to" endpoint of a deeper edge. This is harmless (graphviz just draws the extra edge; `dedupeEdges` still collapses exact repeats) but can occasionally show what looks like a redundant edge back into an already-visible node.
