# Usage Guide: Codeshot

## What This Does

Codeshot answers one question: "what exactly touches this one function?" Point it at a symbol name in a codebase and it produces a diagram (PNG by default) showing everything that calls that symbol and everything that symbol calls — pulled live from the code, not hand-drawn or remembered.

## How to Use It

**One-time setup, per machine:** follow [README.md](README.md#install)'s "Install" section (installs Codeshot, `codegraph`, and `graphviz`, and indexes the repo you want to graph). If `codegraph` or `graphviz` is missing when you run Codeshot, it tells you exactly what's missing and how to install it — you don't need to guess.

**Every time you want a diagram:**

1. `cd` into (or point `--path` at) the repo you want to graph.
2. Run `codeshot <SymbolName>`.
3. Codeshot prints the path to the generated PNG on success — open that file to see the diagram.

Optional flags:
- Point at a different repo: `codeshot <SymbolName> --path /path/to/other/repo`
- Choose where the image is saved: `codeshot <SymbolName> --out ~/Desktop/diagram.png`
- Fetch more callers/callees for a heavily-used symbol: `codeshot <SymbolName> --limit 200` (default is 50)
- Keep the image readable for a heavily-used symbol: `codeshot <SymbolName> --limit 200 --max-render 30` — fetches up to 200 (so the truncation warning stays accurate) but only draws the first 30 distinct callers/callees, instead of a huge image
- Render as SVG instead of PNG: `codeshot <SymbolName> --format svg --out diagram.svg` — stays crisp when you zoom in and keeps text selectable, useful for a diagram you'll want to inspect closely rather than just glance at
- See callers-of-callers / callees-of-callees, not just the direct trail: `codeshot <SymbolName> --depth 2` — each extra hop is drawn in a progressively lighter color so you can tell how far a node is from the symbol; Codeshot fetches this itself (CodeGraph has no multi-hop query of its own), so a heavily-connected symbol at `--depth 3`+ can be slow, and Codeshot will warn on stderr if it hit an internal safety cap before finishing
- If the symbol name itself starts with a dash (rare — e.g. a mangled/generated name), put flags first and separate the name with `--`: `codeshot --path /path/to/repo -- -MangledName`

**Reading the diagram:** boxes are code symbols; the symbol you asked about is highlighted darker. Arrows point in call direction — an arrow into your symbol is a caller, an arrow out is something it calls. Dashed arrows mean the caller is test code, so you can tell "is this only exercised by tests" at a glance.

## What to Do When Something Breaks

- **"codeshot: 'codegraph' not found on PATH"** — Install CodeGraph and make sure it's on your PATH, then try again.
- **"codeshot: 'dot' not found on PATH"** — Install Graphviz (`brew install graphviz` on Mac, `apt install graphviz` on Ubuntu/WSL), then try again.
- **The command runs but the diagram is empty or missing edges** — The repo probably hasn't been indexed yet, or the index is stale. Run `codegraph init` (or re-run indexing) in the target repo first.
- **"Symbol not found" or an empty diagram for a symbol you know exists** — Double-check the exact spelling/casing of the symbol name, and confirm `--path` points at the repo that actually contains it.
- **The image looks unreadable / too cluttered** — This usually means the symbol has a very large number of callers or callees. Rerun with `--max-render <n>` (e.g. `--max-render 30`) to cap how many are drawn — Codeshot will still tell you on stderr how many were left out. If the nodes themselves are legible but hard to read at the zoom level a PNG forces on you, try `--format svg` instead — it stays crisp at any zoom, so it's worth trying before reaching for `--max-render` if you still want to see everything.
- **"codeshot: showing N callers/callees — ... may have cut off more"** — Rerun with a higher `--limit` if you need the full picture (see `TECHNICAL.md` for why this warning can occasionally be a false alarm).
- **"codeshot: --depth traversal stopped early (internal safety cap of 200 discovered nodes)"** — The symbol is heavily connected enough that `--depth` hit an internal limit before finishing; the graph you got is real but incomplete beyond that point. Try a smaller `--depth` (2 instead of 3), a lower `--limit`, or a more specific, less-central symbol.
- **`--depth` runs slowly** — Each additional hop makes one sequential `codegraph` call per newly discovered node (CodeGraph itself has no multi-hop traversal for `callers`/`callees`, so Codeshot does this client-side), so a well-connected symbol at `--depth 2` or higher can take noticeably longer than the default `--depth 1`. This is expected, not a bug.

For anything not covered here, check `TECHNICAL.md` or open an issue on the GitHub repo.

## FAQ

**Do I need to run this from inside the repo I want to graph?**
No — use `--path` to point at any repo. Running from inside it is just the default.

**Does this modify my code or my repo's index?**
No. Codeshot only reads from CodeGraph's existing index and writes an image (and a short-lived temp `.dot` file that it deletes automatically) — it never writes to the repo itself.

**Can I graph a symbol in a repo I haven't indexed yet?**
No — CodeGraph needs to index the repo first (`codegraph init`) before Codeshot has anything to query.

**Where does the output file go if I don't specify `--out`?**
Your system's temp directory, with a name like `callgraph-<Symbol>-<timestamp>.png` (or `.svg`, etc. if you passed `--format`). Codeshot always prints the exact path so you don't have to guess.

**What output formats does `--format` support?**
Anything the `dot` binary on your system supports via `-T<format>` — `png` (default), `svg`, `pdf`, and many more. Run `dot -T` with no argument to see the exact list your graphviz install supports.
