# Technical Reference: Codeshot

## Architecture

Codeshot is a single-file CLI (`render/callgraph.js`) with no server, no persistent state, and no dependencies beyond two external binaries it shells out to.

```
argv (symbol, --path, --out)
        |
        v
requireOnPath('codegraph')  --- exits with an install hint if missing
requireOnPath('dot')        --- exits with an install hint if missing
        |
        v
runCodegraph(['callers', symbol, '--path', repoPath, '--json'])
runCodegraph(['callees', symbol, '--path', repoPath, '--json'])
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

- **`render/callgraph.js`** — the entire tool. Exports `buildDot(symbol, callers, callees)` (pure: turns caller/callee arrays into a DOT digraph string) and `isTestRef(node)` (true if a node's name or filePath looks test-related, used to render those edges dashed). Everything else (`requireOnPath`, `runCodegraph`, `main`) is CLI plumbing, not exported.
- **`test/run.js`** — assertion-based test suite (Node's built-in `assert`, no framework) covering `buildDot` and `isTestRef` directly. Run via `npm test`.
- **`package.json`** — declares the `codeshot` bin pointing at `render/callgraph.js`, and the `test` script.
- **`.runechoguardignore`** — false-positive suppression list for the RunEcho pre-commit symbol-resolution guard (a local hook, not part of codeshot itself). Bare-call identifiers the guard can't resolve (e.g. Node builtins passed as function parameters) get listed here instead of disabling the guard.

## External Dependencies

- **[`codegraph`](https://github.com/colbymchenry/codegraph) CLI** — must be on `PATH`; the target repo must already be indexed (`codegraph init`).
- **`dot` (Graphviz)** — must be on `PATH`.

Both are checked at startup via `which`/`where` (see the pipeline diagram above); a missing binary prints an install hint and exits 1 rather than failing deep in the call stack.

## Configuration

No environment variables, no config file. All behavior is controlled by CLI arguments:

| Flag | Default | Purpose |
|---|---|---|
| `<symbol>` (positional, required) | — | The symbol to graph |
| `--path` | `.` (cwd) | Repo path passed through to `codegraph` |
| `--out` | `<tmpdir>/callgraph-<symbol>-<timestamp>.png` | Output PNG path |

## Maintenance Commands

```bash
npm test              # runs test/run.js — 7 assertions against buildDot/isTestRef
node render/callgraph.js <symbol> --path <repo> --out /tmp/out.png   # manual smoke test
```

There is no service to restart, no rollback beyond `npm uninstall -g codeshot` / reinstalling a prior git ref, and no other services, logs, or scheduled jobs to maintain — see [README.md](README.md#install) for the install command itself.

## Known Limitations

- No automated test covers the actual `dot`/`codegraph` shell-out path — `test/run.js` only exercises the pure `buildDot`/`isTestRef` logic. A missing or incompatible `codegraph`/`dot` binary is only caught by running the CLI itself.
- `buildDot` does not deduplicate repeated caller/callee entries; if `codegraph`'s JSON contains duplicates, the rendered graph will show duplicate edges.
- `isTestRef` matches on the substring `test` (case-insensitive) in either the node name or file path — this will misfire on any production symbol or path that happens to contain "test" (e.g. `TestConnectionPool`, `src/attestation.js`).
- No handling for extremely large call graphs (hundreds of callers/callees) — `dot`'s default layout may become unreadable; there's no `--depth` or filtering flag.
