# Codeshot

**A picture of what calls what.** Renders a symbol's [CodeGraph](https://github.com/colbymchenry/codegraph) call trail (who calls it, what it calls) as an image.

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
- [`codegraph`](https://github.com/colbymchenry/codegraph) CLI on PATH, with the target repo indexed (`codegraph init`)
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
- `--format` — output format, passed straight to `dot -T<fmt>` (defaults to `png`). `svg` is a good alternative for large graphs — it stays crisp at any zoom level and keeps text selectable, unlike a raster PNG. Any format `dot -T` supports works; an unsupported one fails with `dot`'s own error listing the valid ones.
- `--depth` — how many hops of callers-of-callers / callees-of-callees to draw beyond the direct trail (defaults to `1`, i.e. today's direct-only behavior; must be a positive integer). Codeshot fetches this itself, one sequential `codegraph` call per newly discovered node — CodeGraph has no multi-hop traversal of its own for `callers`/`callees`. Each additional hop is drawn in a progressively lighter shade so you can tell how far a node is from the symbol at a glance. There's an internal, non-configurable safety cap on total nodes discovered (a well-connected symbol at `--depth 3`+ can otherwise mean hundreds of sequential `codegraph` calls); Codeshot warns on stderr if it hit that cap before finishing — see [TECHNICAL.md](TECHNICAL.md#configuration) for the exact number and rationale.

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
