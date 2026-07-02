# Codeshot

**A picture of what calls what.** Renders a symbol's [CodeGraph](https://github.com/colbymchenry/codegraph) call trail (who calls it, what it calls) as an image.

---

## What it does

```bash
codeshot RollAutoSnapshot --path ~/code/myrepo --out callgraph.png
```

Pulls the symbol's callers/callees straight from CodeGraph's index (`codegraph callers`/`callees --json`) and renders them through [graphviz](https://graphviz.org/) — a real diagram, not hand-drawn ASCII. Test callers are shown dashed so production call paths stand out.

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
codeshot <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>]
```

- `--path` — repo to query (defaults to cwd)
- `--out` — output file (defaults to a temp PNG; path is printed on success)
- `--limit` — max callers/callees to fetch from CodeGraph (defaults to 50). CodeGraph itself defaults to 20 and gives no "N of M" total, so if the result hits the limit, Codeshot prints a warning that more may exist — rerun with a higher `--limit` if so.

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
