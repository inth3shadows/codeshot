# Technical Reference: Codeshot

## Architecture

Codeshot is a single-file CLI (`render/callgraph.js`) with no server, no persistent state, and no dependencies beyond two external binaries it shells out to.

```
argv (symbol, --path, --out, --limit)
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
truncationWarning(...) --- warns on stderr if a result hit --limit exactly
        |
        v
buildDot(symbol, callers, callees)  --- pure function, produces a DOT string
        |
        v
write DOT to a tempfile -> `dot -Tpng <tempfile> -o <outFile>` -> delete tempfile
        |
        v
print outFile path to stdout
```

See [README.md](README.md#design-decisions)'s "Design decisions" section for why this shells out to the CodeGraph CLI instead of reading its SQLite index, and why graphviz instead of a JS graph-drawing library.

**Why is `buildDot` exported from a file that also runs as a CLI?**
`render/callgraph.js` guards its `main()` call with `if (require.main === module)`, so `node render/callgraph.js <symbol>` still runs the CLI, but `require('./render/callgraph.js')` (used by `test/run.js`) gets `{ buildDot, isTestRef }` without executing anything. This keeps the test suite dependency-free — no test framework, no mocking of `execFileSync`.

## File Descriptions

- **`render/callgraph.js`** — the entire tool. Exports `buildDot(symbol, callers, callees)` (pure: turns caller/callee arrays into a DOT digraph string, deduplicating entries with the same `name`+`filePath` so repeated JSON rows don't render as duplicate edges), `isTestRef(node)` (true if a node's name or filePath looks test-related, used to render those edges dashed), and `truncationWarning(kind, results, limit)` (pure: returns a warning string if `results.length` hit `limit` exactly, else `null`). Everything else (`requireOnPath`, `runCodegraph`, `main`, `dedupeNodes`) is CLI plumbing, not exported.
- **`test/run.js`** — assertion-based test suite (Node's built-in `assert`, no framework) covering `buildDot`, `isTestRef`, and `truncationWarning` directly. Run via `npm test`.
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
| `--path` | `.` (cwd) | Repo path passed through to `codegraph` |
| `--out` | `<tmpdir>/callgraph-<symbol>-<timestamp>.png` | Output PNG path |
| `--limit` | `50` | Max callers/callees fetched from `codegraph` (its own CLI default is 20). Must be a positive integer — rejected with an error otherwise, since `codegraph` silently returns an empty result for a malformed limit rather than erroring itself. `codegraph`'s JSON has no total/truncated field, so Codeshot's only signal that more may exist is the result count hitting `--limit` exactly — when it does, a warning is printed to stderr. That heuristic false-positives for a symbol with exactly `--limit` real results and no more; there's no way to distinguish "exactly complete" from "truncated" without a total field from `codegraph`. |

## Maintenance Commands

```bash
npm test              # runs test/run.js — assertions against buildDot/isTestRef/truncationWarning
node render/callgraph.js <symbol> --path <repo> --out /tmp/out.png   # manual smoke test
```

There is no service to restart, no rollback beyond `npm uninstall -g codeshot` / reinstalling a prior git ref, and no other services, logs, or scheduled jobs to maintain — see [README.md](README.md#install) for the install command itself.

## Known Limitations

- `codegraph`/`codeshot`'s own JSON<->CLI contract is only covered indirectly — `test/run.js` exercises `buildDot`'s output against the real `dot` binary (catches malformed DOT syntax) but does not run against a real `codegraph` index; a `codegraph` output-shape change is only caught by running the CLI itself.
- `isTestRef` is a naming-convention heuristic (word-boundary `Test`/`Spec` prefix or suffix in the name, or a `test`/`tests`/`spec`/`__tests__` directory or `.test.`/`.spec.` filename), not a semantic check — a production symbol that happens to follow test-like naming (e.g. a function literally named `Test`) would still be misclassified.
- No handling for extremely large call graphs — `--limit` controls how many results are *fetched*, but there's still no way to cap or filter how many are *rendered*; a symbol with hundreds of real callers produces a very tall, if technically complete-within-`--limit`, image. There's also no `--depth` flag for multi-hop traversal.
