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
- See callers-of-callers / callees-of-callees, not just the direct trail: `codeshot <SymbolName> --depth 2` — each extra hop is drawn in a progressively lighter color so you can tell how far a node is from the symbol; Codeshot fetches this itself (CodeGraph has no multi-hop query of its own), so a heavily-connected symbol at `--depth 3`+ can be slow, and Codeshot will warn on stderr if it hit its node-discovery safety cap before finishing (raise it with `--max-depth-nodes`, default `200`)
- If the symbol name itself starts with a dash (rare — e.g. a mangled/generated name), put flags first and separate the name with `--`: `codeshot --path /path/to/repo -- -MangledName`

**Reading the diagram:** boxes are code symbols; the symbol you asked about is highlighted darker. Arrows point in call direction — an arrow into your symbol is a caller, an arrow out is something it calls. Dashed arrows mean the caller is test code, so you can tell "is this only exercised by tests" at a glance. A dotted gray arrow labeled "file" means CodeGraph could only trace a module-level/import reference, not an actual function call site (common with dependency-injection patterns) — treat it with more skepticism than a solid arrow.

## Generating a whole-repo architecture diagram

This is a different question from "what touches this symbol" — it's "how do
the files in this repo depend on each other." Run:

```bash
codeshot --architecture --path /path/to/repo --out architecture.svg --format svg
```

Codeshot enumerates every symbol CodeGraph knows about in that repo, probes
each one's callees, and rolls the results up into a file-to-file graph — a
box per file, an arrow per pair of files with at least one call between
them, labeled with how many calls. Boxes for test files render dashed, same
as symbol mode. Unlike symbol mode, there's no `<SymbolName>` argument — the
`--architecture` flag replaces it, and combining the two is rejected.

**Too many files to read? Group them.** On anything bigger than a handful of
files, a box per file is a hairball. `--group-depth <n>` draws directories
instead — files roll up into their first `n` path segments, and the call counts
between them are summed:

```bash
codeshot --architecture --path /path/to/repo --group-depth 1 --out modules.svg --format svg
```

`--group-depth 1` gives you the top-level module map (`src/` → `lib/`,
`test/` → `src/`); `--group-depth 2` splits one level finer (`src/api/` →
`src/db/`). Calls *inside* a group don't draw an arrow — the diagram is about
coupling between modules, the same reason calls inside one file don't draw one.
Prefer this over `--max-render` when the graph is dense: `--max-render` throws
the quiet files away entirely, while grouping keeps every *cross-module* call
and just zooms out. Calls between two files in the same group don't draw an
arrow, so the weights on a grouped diagram add up to less than the per-file
one's — that difference is the repo's intra-module traffic, not a dropped edge.
Files at the repo root have no directory to roll into, so they stay as
themselves.

This can genuinely take a few minutes on a mid-size-or-larger repo (one
`codegraph` call per symbol, run sequentially) — `--max-symbols <n>`
(default 500) trades completeness for speed if you want a faster, partial
scan; Codeshot tells you on stderr if it stopped early. Note what "partial"
means: symbols are probed in path order, so the cut is a prefix, not a sample —
files late in path order go unprobed and can therefore appear to call nothing
when they really do. Raise `--max-symbols` before trusting a sparse-looking
corner of a big repo's diagram. `--limit` and
`--max-render` carry over from symbol mode (see above); `--depth` doesn't
apply here and is rejected if you pass it.

## Embedding a diagram in your docs, and keeping it fresh

Both modes take `--embed <file.md>` to write the diagram straight into an
existing markdown doc and keep it refreshed in place — the same idempotent
HTML-comment-marker approach `doctoc`/`terraform-docs` use:

```bash
codeshot --architecture --path . --embed TECHNICAL.md --format svg
```

This writes the image to a stable path next to the doc
(`codeshot-arch-<repo>.svg`) and inserts (or, on re-runs, updates in place) a
block:

```markdown
<!-- codeshot:arch:start -->
![<repo> architecture — generated by codeshot](codeshot-arch-<repo>.svg)
<!-- codeshot:arch:end -->
```

Symbol mode works the same way, keyed by the symbol name
(`<!-- codeshot:<symbol>:start -->`), so several distinct diagrams can live in
one doc without clobbering each other. `--group-depth` gets its own key too
(`<!-- codeshot:arch-d1:start -->`), so you can commit both the per-file and the
per-module architecture picture in the same doc and `--check` both. `--embed` **refreshes an existing doc —
it won't create one**, and a stray/half-present marker pair is an error rather
than a silent mangle.

**Stop it going stale — `--check`.** A committed diagram silently rots the
moment the code changes. Add `--check` to *verify* the committed image and the
doc's block are current (regenerating in memory, mutating nothing) — exit `0`
if up to date, exit `1` (with what's stale) if not:

```bash
codeshot --architecture --path . --embed TECHNICAL.md --format svg --check
```

Drop that into CI or a pre-commit hook to fail the build when someone changes
the code but not the diagram. For `svg` output (the recommended `--embed`
format) `--check` compares the diagram's **structure** — the set of nodes and
call edges — not the raw rendered bytes, so it is **graphviz-version
independent**: the committed image and the CI machine can run different
`graphviz` builds without a spurious mismatch. By design it only fails on
*structural* drift (a caller/callee/edge appearing or disappearing); a
cosmetic-only change with the identical graph — e.g. a re-color — is not
flagged. Non-`svg` formats (png, svgz, …) have no recoverable structure and
fall back to a raw byte-compare, which does require CI to use the same
`graphviz` version that generated the committed image.

## What to Do When Something Breaks

- **"codeshot: 'codegraph' not found on PATH"** — Install CodeGraph and make sure it's on your PATH, then try again.
- **"codeshot: codegraph has no index for '...' yet"** — CodeGraph is installed but this repo has never been indexed. Run the exact command the message gives you (`codegraph init <path>`), then rerun codeshot. Codeshot reads CodeGraph's index; it deliberately doesn't build one for you (indexing is a heavy, persistent operation and CodeGraph's call to make).
- **"codeshot: 'dot' not found on PATH"** — Install Graphviz (`brew install graphviz` on Mac, `apt install graphviz` on Ubuntu/WSL), then try again.
- **The command runs but the diagram is empty or missing edges** — The repo probably hasn't been indexed yet, or the index is stale. Run `codegraph init` (or re-run indexing) in the target repo first. Codeshot no longer draws a blank picture silently: it warns on stderr in the two cases below.
- **"codeshot: '...' has no callers or callees in codegraph's index"** — The symbol exists but nothing calls it and it calls nothing, so the diagram is just that one box. It may be genuinely unused (dead code) or a top-level entry point — or codegraph's index is incomplete for its file (see the sparse-diagram note below). The image is still written; the warning just explains why it's a lone box.
- **"codeshot: --architecture found no cross-file call edges — the diagram is blank"** — codegraph reported no resolved calls *between files* in this repo, so there's nothing for the file-level graph to draw. Expected for a small or single-file repo; otherwise the index is likely missing or stale — run `codegraph init <path>`, then `codegraph status` to confirm it built. A blank image is still written so the `--out` path exists.
- **"codeshot: symbol '...' not found in codegraph's index"** — Double-check the exact spelling/casing of the symbol name, and confirm `--path` points at the repo that actually contains it.
- **"codeshot: N symbol name(s) appear in more than one file ... N file name(s) appear in more than one directory"** — `--architecture` only. The first half is informational: those names collide across files, so Codeshot re-probed them with a file-qualified `codegraph node -f` and their edges are attributed correctly. Where that probe can't answer completely (CodeGraph truncates its call list at 12 entries, among other cases) Codeshot silently falls back to the bare-name probe, which over-reports rather than under-reports — so a colliding name with a very large fan-out can still show extra edges. The second half is a real caveat you can't turn off: two files sharing a basename (two `index.js`) can only be probed by that bare name, so edges involving them may land on the wrong one. Rename one of the files, or treat those specific edges as unverified.
- **The diagram is real but looks sparse — CodeGraph's index has known gaps.** Tested against several real codebases: same-named methods on unrelated classes are sometimes merged or one silently dropped, aliased imports (`import x as y`) can return zero callers for a genuinely well-used function, and dependency-injection patterns (e.g. FastAPI's `Depends()`) often don't resolve to real caller functions at all. Codeshot only draws what CodeGraph reports — if a diagram looks thinner than you expect for a symbol you know is heavily used, that's more likely a CodeGraph indexing gap than a Codeshot bug. A `dotted gray "file"` edge (see above) is one visible symptom of this; a *missing* edge is the invisible version and harder to catch — spot-check against the real source if it matters.
- **`--out diagram.svg` produced a PNG (or vice versa)** — Codeshot only ever writes what `--format` says; it never infers format from `--out`'s extension. If you see this, you forgot `--format svg` (or whichever format matches the extension you wanted) — Codeshot now warns about this mismatch on stderr before it happens, so check for that warning first.
- **The image looks unreadable / too cluttered** — This usually means the symbol has a very large number of callers or callees. Rerun with `--max-render <n>` (e.g. `--max-render 30`) to cap how many are drawn — Codeshot will still tell you on stderr how many were left out. If the nodes themselves are legible but hard to read at the zoom level a PNG forces on you, try `--format svg` instead — it stays crisp at any zoom, so it's worth trying before reaching for `--max-render` if you still want to see everything.
- **"codeshot: showing N callers/callees — ... may have cut off more"** — Rerun with a higher `--limit` if you need the full picture (see `TECHNICAL.md` for why this warning can occasionally be a false alarm).
- **"codeshot: --depth traversal stopped early (safety cap of 200 discovered nodes)"** — The symbol is heavily connected enough that `--depth` hit its node-discovery cap before finishing; the graph you got is real but incomplete beyond that point. Try a smaller `--depth` (2 instead of 3), a lower `--limit`, a more specific, less-central symbol, or raise the cap itself with `codeshot <Symbol> --depth 3 --max-depth-nodes 500` (default is 200).
- **`--depth` runs slowly** — Each additional hop makes one sequential `codegraph` call per newly discovered node (CodeGraph itself has no multi-hop traversal for `callers`/`callees`, so Codeshot does this client-side), so a well-connected symbol at `--depth 2` or higher can take noticeably longer than the default `--depth 1`. This is expected, not a bug.
- **`--architecture` is taking a long time** — Expected on anything past a small repo: it's one sequential `codegraph` call per enumerated symbol, and there's no way to parallelize it (concurrent `codegraph` calls against one index race and fail). Rerun with a smaller `--max-symbols` (e.g. `--max-symbols 100`) for a faster, partial scan — Codeshot warns on stderr when the scan is cut short by the cap so you know the result is incomplete.
- **`--architecture`'s diagram is a hairball / unreadable** — Reach for `--group-depth 1` first: it draws one box per top-level directory instead of one per file, so no *module-to-module* call is thrown away, the picture just zooms out (see "Generating a whole-repo architecture diagram" above). If it's still dense at group level, `--max-render <n>` (e.g. `--max-render 20`) then keeps only the busiest N nodes by total call-edge weight and drops the rest. Some remaining nodes can end up with no surviving edges if all their edges pointed at a dropped one — that's expected, not a bug, at aggressive `--max-render` values.
- **"codeshot: --group-depth N left no edges to draw"** — Every call in the repo is between files that share a group at that depth, so the grouped diagram is blank. The message tells you which of the two causes it is. *"every file falls into a single group"* (common at `--group-depth 1` where everything lives under `src/`) means the depth is too coarse — try 2 or 3. *"the N groups at this depth have no calls between them"* means the modules genuinely don't call each other at this depth; going deeper will stay blank, so drop the flag for the per-file graph.

For anything not covered here, check `TECHNICAL.md` or open an issue on the GitHub repo.

## FAQ

**Do I need to run this from inside the repo I want to graph?**
No — use `--path` to point at any repo. Running from inside it is just the default.

**Does this modify my code or my repo's index?**
No. Codeshot only reads from CodeGraph's existing index and writes an image (and a short-lived temp `.dot` file that it deletes automatically) — it never writes to the repo itself.

**Can I graph a symbol in a repo I haven't indexed yet?**
No — CodeGraph needs to index the repo first (`codegraph init`) before Codeshot has anything to query.

**Where does the output file go if I don't specify `--out`?**
Your system's temp directory, with a name like `callgraph-<Symbol>-<timestamp>.png` (or `arch-<repoName>-<timestamp>.png` for `--architecture`; either with `.svg` etc. if you passed `--format`). Codeshot always prints the exact path so you don't have to guess.

**What output formats does `--format` support?**
Anything the `dot` binary on your system supports via `-T<format>` — `png` (default), `svg`, `pdf`, and many more. Run `dot -T` with no argument to see the exact list your graphviz install supports.
