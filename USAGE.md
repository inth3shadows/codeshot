# Usage Guide: Codeshot

## What This Does

Codeshot answers one question: "what exactly touches this one function?" Point it at a symbol name in a codebase and it produces a PNG diagram showing everything that calls that symbol and everything that symbol calls — pulled live from the code, not hand-drawn or remembered.

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
- If the symbol name itself starts with a dash (rare — e.g. a mangled/generated name), put flags first and separate the name with `--`: `codeshot --path /path/to/repo -- -MangledName`

**Reading the diagram:** boxes are code symbols; the symbol you asked about is highlighted darker. Arrows point in call direction — an arrow into your symbol is a caller, an arrow out is something it calls. Dashed arrows mean the caller is test code, so you can tell "is this only exercised by tests" at a glance.

## What to Do When Something Breaks

- **"codeshot: 'codegraph' not found on PATH"** — Install CodeGraph and make sure it's on your PATH, then try again.
- **"codeshot: 'dot' not found on PATH"** — Install Graphviz (`brew install graphviz` on Mac, `apt install graphviz` on Ubuntu/WSL), then try again.
- **The command runs but the diagram is empty or missing edges** — The repo probably hasn't been indexed yet, or the index is stale. Run `codegraph init` (or re-run indexing) in the target repo first.
- **"Symbol not found" or an empty diagram for a symbol you know exists** — Double-check the exact spelling/casing of the symbol name, and confirm `--path` points at the repo that actually contains it.
- **The PNG looks unreadable / too cluttered** — This usually means the symbol has a very large number of callers or callees. There's currently no way to filter or limit depth; try graphing a more specific, less-central symbol instead.
- **"codeshot: showing N callers/callees — ... may have cut off more"** — Rerun with a higher `--limit` if you need the full picture (see `TECHNICAL.md` for why this warning can occasionally be a false alarm).

For anything not covered here, check `TECHNICAL.md` or open an issue on the GitHub repo.

## FAQ

**Do I need to run this from inside the repo I want to graph?**
No — use `--path` to point at any repo. Running from inside it is just the default.

**Does this modify my code or my repo's index?**
No. Codeshot only reads from CodeGraph's existing index and writes a PNG (and a short-lived temp `.dot` file that it deletes automatically) — it never writes to the repo itself.

**Can I graph a symbol in a repo I haven't indexed yet?**
No — CodeGraph needs to index the repo first (`codegraph init`) before Codeshot has anything to query.

**Where does the output file go if I don't specify `--out`?**
Your system's temp directory, with a name like `callgraph-<Symbol>-<timestamp>.png`. Codeshot always prints the exact path so you don't have to guess.
