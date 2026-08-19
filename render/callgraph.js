#!/usr/bin/env node
'use strict';

/**
 * Render a symbol's caller/callee trail — or a whole repo's file-level
 * dependency graph (--architecture) — as an image.
 *
 * Pulls structured data from the CodeGraph CLI (`codegraph callers`/`callees`/
 * `query --json`) and renders it through graphviz (`dot`). Requires both on PATH.
 *
 * Usage:
 *   node render/callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--format <fmt>] [--depth <n>]
 *   node render/callgraph.js --architecture [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--max-symbols <n>] [--format <fmt>]
 */

const { execFileSync, execFile } = require('child_process');
const { parseArgs, promisify } = require('util');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

const execFileAsync = promisify(execFile);

const DEFAULT_LIMIT = 50;

// Node tooltips (the file a symbol lives in, shown on hover) only render in
// graphviz's svg-family output, where they become <a xlink:title>. Any other
// format ignores them, so buildDot is told to emit them only for these.
const SVG_TOOLTIP_FORMATS = new Set(['svg', 'svgz']);

// --check compares the graph STRUCTURE (node/edge set) rather than raw image
// bytes for these formats — see svgStructure. Only plain svg qualifies: it's
// text we can parse, and its <title>s carry the semantic ids version-independently.
// svgz (gzipped) and raster formats have no recoverable structure, so they fall
// back to the byte-compare and its same-graphviz-version caveat.
const STRUCTURAL_CHECK_FORMATS = new Set(['svg']);

// Multi-hop traversal (--depth > 1) makes one sequential codegraph call per
// newly discovered node, per hop — on a well-connected symbol that fans out
// fast. This caps total discovered nodes across both directions combined so
// one request can't turn into hundreds of sequential codegraph invocations.
// Exposed as --max-depth-nodes (default below) for a symbol whose real graph
// genuinely needs a higher cap to finish at --depth 3+.
const DEFAULT_NODE_BUDGET = 200;

// --architecture probes every enumerated symbol sequentially (one codegraph
// call each) to build the file-level graph — a multi-minute operation on a
// mid-size repo, the same reasoning --max-depth-nodes above now shares: users
// legitimately need to trade coverage for speed themselves, so both are
// exposed as flags (--max-symbols here, --max-depth-nodes for --depth).
const DEFAULT_MAX_SYMBOLS = 500;

function requireOnPath(bin, installHint) {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin], { stdio: 'ignore' });
  } catch {
    console.error(`codeshot: '${bin}' not found on PATH. ${installHint}`);
    process.exit(1);
  }
}

// codegraph prints this as human-readable text (with an ANSI-colored icon),
// not JSON, when a queried symbol isn't in its index at all — as opposed to
// a genuine JSON-shape error, this is a common, expected case (typo, wrong
// --path) worth its own clean message instead of a raw "did not return JSON".
function matchSymbolNotFound(out) {
  const m = String(out).match(/Symbol\s+["'“”](.+?)["'“”]\s+not found/i);
  return m ? m[1] : null;
}

// codegraph prints "✗ CodeGraph not initialized in <path> — Run 'codegraph init'
// first" (plain text, not JSON) when --path points at a repo it has never
// indexed — the single most common first-run failure. Without special-casing it,
// the JSON.parse fallback wraps that message in a confusing "did not return JSON"
// line; here it earns its own clean, actionable message instead. Sibling of
// matchSymbolNotFound. Deliberately does NOT auto-run 'codegraph init' — building
// an index is a heavy, persistent side effect and codegraph's call to make, not
// codeshot's (same detect-and-instruct stance as requireOnPath).
function matchNotInitialized(out) {
  return /CodeGraph\s+not\s+initialized/i.test(String(out));
}

// The repo path codeshot passed to codegraph, recovered from the arg array so an
// error message can name it. Every codegraph invocation includes '--path <p>'.
function argRepoPath(args) {
  const i = args.indexOf('--path');
  return i !== -1 && args[i + 1] !== undefined ? args[i + 1] : '.';
}

// Shared clean exit for the unindexed-repo case. Reached ONLY from runCodegraph's
// catch, matched against codegraph's STDERR on a non-zero exit — never against a
// successful command's stdout. That distinction is load-bearing: codegraph's
// enumerate query (`query -- ''`) returns every indexed node, whose content can
// legitimately include the literal phrase "CodeGraph not initialized" (e.g. this
// file's own source describing the message). Scanning stdout for it would false-
// positive on a repo that is perfectly well indexed. Returns null for non-fatal
// callers, exits 1 otherwise — same contract as the matchSymbolNotFound branch.
function exitNotInitialized(args, fatal) {
  if (!fatal) return null;
  const repoPath = argRepoPath(args);
  console.error(`codeshot: codegraph has no index for '${repoPath}' yet — build one first with 'codegraph init ${repoPath}', then rerun. (codeshot reads codegraph's index; it doesn't create it.)`);
  process.exit(1);
}

// `fatal` (default true) matches every existing call site's behavior. Pass
// `fatal: false` when calling this in a loop that probes many symbols and
// must survive an individual bad one (e.g. --architecture's enumeration) —
// process.exit() would otherwise kill the whole scan, and a bare try/catch
// around runCodegraph does NOT catch process.exit().
// `json: false` returns the raw stdout instead of parsing it. Needed because
// `codegraph node` is the one subcommand codeshot calls that has no --json flag
// (callers/callees/query all do) — its output is markdown-ish text only. The
// not-found and not-initialized handling above it is identical either way, which
// is why this is an option here rather than a separate parallel runner.
function parseCodegraphOutput(out, args, { fatal = true, json = true } = {}) {
  // Scoped to the FIRST non-empty line, never the whole response. codegraph
  // prints the not-found message alone, on line one — but under `json: false`
  // a successful response embeds arbitrary indexed source, which can contain
  // that sentence as content (this repo's own test/run.js contains the literal
  // string). Scanning the whole body is the same false-positive trap that keeps
  // the not-initialized check off stdout entirely; see runCodegraph.
  const firstLine = String(out).split('\n').find(l => l.trim().length > 0) || '';
  const notFound = matchSymbolNotFound(firstLine);
  if (notFound !== null) {
    if (!fatal) return null;
    console.error(`codeshot: symbol '${notFound}' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.`);
    process.exit(1);
  }
  if (!json) return out;
  // NOTE: the "not initialized" case is deliberately NOT handled here on stdout —
  // see exitNotInitialized. A successful codegraph response can contain that
  // phrase as indexed source content; it's only a real signal on stderr with a
  // non-zero exit, which runCodegraph handles.
  try {
    return JSON.parse(out);
  } catch {
    if (!fatal) return null;
    console.error(`codeshot: 'codegraph ${args.join(' ')}' did not return JSON:\n${out.trim()}`);
    process.exit(1);
  }
}

// Node's execFile default maxBuffer (1MB) is too small once --architecture's
// enumeration query returns every symbol in a mid-size-or-larger repo as JSON
// (confirmed: exceeded on a real 1,870-node index) — raised well above any
// single codegraph response this tool realistically produces.
const MAX_CODEGRAPH_BUFFER = 64 * 1024 * 1024;
async function runCodegraph(args, { fatal = true, json = true } = {}) {
  let result;
  try {
    result = await execFileAsync('codegraph', args, { encoding: 'utf8', maxBuffer: MAX_CODEGRAPH_BUFFER });
  } catch (err) {
    // codegraph exits NON-ZERO for an unindexed repo, printing "CodeGraph not
    // initialized" to STDERR. Match only stderr — never err.stdout — because a
    // partial stdout on some other failure could contain that phrase as indexed
    // source content and false-positive (the same trap that stdout scanning in
    // parseCodegraphOutput would be). Surface this known first-run state cleanly;
    // re-throw anything else so a genuine codegraph failure isn't misreported.
    if (matchNotInitialized(`${err.stderr || ''}`)) return exitNotInitialized(args, fatal);
    throw err;
  }
  return parseCodegraphOutput(result.stdout, args, { fatal, json });
}

// `codegraph status` warns when an index was left mid-build ("N references from
// an interrupted run are awaiting resolution") — a state where callers/callees
// silently return incomplete/empty results. codeshot can't tell that apart from
// a genuinely edge-free repo by node count alone, so it reads the count straight
// from status. Pure: parses the plain-text status output, returns the integer or
// null. Tolerates the thousands-separator commas and surrounding ANSI codes.
function parseUnresolvedRefs(statusOutput) {
  const m = String(statusOutput).match(/([\d,]+)\s+references?\s+from an interrupted run/i);
  if (!m) return null;
  const n = Number(m[1].replace(/,/g, ''));
  return Number.isInteger(n) && n > 0 ? n : null;
}

// Preflight health check: an interrupted index yields a silently-partial (or
// empty) diagram, so warn — with the actual fix — rather than draw a lie. Uses
// `status`'s own count; failure to run status must never block rendering, so a
// throw just means "no warning". A bare `codegraph sync` heals a wedged index
// (it sweeps orphaned refs), though it can misleadingly print "Already up to
// date" while doing so — so the advice is to fix, then re-confirm via status.
function indexHealthWarning(repoPath) {
  let out;
  try {
    out = execFileSync('codegraph', ['status', repoPath], { encoding: 'utf8', maxBuffer: MAX_CODEGRAPH_BUFFER });
  } catch {
    return null;
  }
  const n = parseUnresolvedRefs(out);
  if (n === null) return null;
  return `codeshot: codegraph's index has ${n} unresolved reference(s) from an interrupted run — callers/callees may be incomplete, so this diagram can be silently partial. Fix with 'codegraph sync ${repoPath}' (or 'codegraph index' for a full rebuild), then confirm 'codegraph status' shows 0 before trusting the diagram.`;
}

function sanitizeForFilename(s) {
  return String(s).replace(/[\\/:*?"<>|]/g, '_');
}

function splitWords(s) {
  return s.replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[\s_-]+/).filter(Boolean);
}

function isTestRef(node) {
  const words = splitWords(String(node.name || ''));
  const firstWord = words[0] || '';
  const lastWord  = words[words.length - 1] || '';
  const nameLooksLikeTest = /^test$/i.test(firstWord) || /^tests?$/i.test(lastWord) || /^specs?$/i.test(lastWord);

  const filePath = String(node.filePath || '');
  const segments = filePath.split(/[\\/]/);
  const filename = segments[segments.length - 1] || '';
  const inTestDir = segments.some(seg => /^(tests?|__tests__|spec)$/i.test(seg));
  const testFilename = /[._-](tests?|specs?)\.[^.]+$/i.test(filename);

  return nameLooksLikeTest || inTestDir || testFilename;
}

function truncationWarning(kind, results, limit) {
  if (!Array.isArray(results) || results.length < limit) return null;
  return `codeshot: showing ${results.length} ${kind} — codegraph's --limit (${limit}) may have cut off more; rerun with --limit <n> to see additional ${kind}.`;
}

// A symbol with no callers AND no callees renders as a lone box — a valid but
// uninformative picture. Warn so the empty result reads as a finding (the symbol
// is unused, an entry point, or codegraph's index is incomplete for its file)
// rather than looking like a tool glitch — the same "warn, don't hand back a
// silent blank" stance as indexHealthWarning/duplicateNameWarning.
function emptyGraphWarning(symbol, callers, callees) {
  const nCallers = Array.isArray(callers) ? callers.length : 0;
  const nCallees = Array.isArray(callees) ? callees.length : 0;
  if (nCallers > 0 || nCallees > 0) return null;
  return `codeshot: '${symbol}' has no callers or callees in codegraph's index — the diagram is just the symbol itself. It may be unused (dead code) or an entry point, or codegraph's index may be incomplete for its file.`;
}

function dedupeNodes(nodes) {
  const seen = new Set();
  const result = [];
  for (const n of nodes) {
    const key = `${n.name} ${n.filePath}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(n);
  }
  return result;
}

function dedupeEdges(edges) {
  const seen = new Set();
  const result = [];
  for (const e of edges) {
    const key = `${e.from.name} ${e.from.filePath} -> ${e.to.name} ${e.to.filePath}`;
    if (seen.has(key)) continue;
    seen.add(key);
    result.push(e);
  }
  return result;
}

// Depth 1 (direct callers/callees) keeps the default edge color set below;
// this only covers hop >= 2, fading lighter the further a node is from the
// symbol so "how far away is this" reads visually, not just as more boxes.
const DEPTH_COLORS = ['#b8c2d0', '#cbd5e1', '#dde3ec'];
function depthColor(depth) {
  return DEPTH_COLORS[Math.min(depth - 2, DEPTH_COLORS.length - 1)];
}

// --max-render is meant to bound the whole picture, not each dimension of it
// separately — so callers, callees, and transitive edges draw from ONE shared
// allowance (spent in that priority order: the direct trail first, deeper
// hops last) rather than each independently getting up to maxRender.
function allocateRenderBudget(maxRender, counts) {
  if (!Number.isFinite(maxRender)) return counts.slice();
  let remaining = maxRender;
  return counts.map(count => {
    const allotted = Math.max(0, Math.min(count, remaining));
    remaining -= allotted;
    return allotted;
  });
}

// codegraph's callers/callees sometimes report a "kind":"file" entry instead
// of a real function — a module-level/import reference, not an actual call
// site. Styling it like any other edge would misrepresent it as a real call,
// so it gets its own dotted/gray/"file" look, distinct from the dashed/"test"
// style below (which only applies to real function nodes).
function edgeStyleAttrs(node, colorOverride) {
  if (node.kind === 'file') return ['style=dotted', 'color="#9ca3af"', 'label="file"'];
  const attrs = colorOverride ? [`color="${colorOverride}"`] : [];
  if (isTestRef(node)) attrs.push('style=dashed', 'label="test"');
  return attrs;
}

// Graphviz keys a node by the exact string used as its id in an edge, so two
// distinct symbols that happen to share a name — the same function name in two
// different files, exactly what --architecture's duplicateNameWarning flags —
// collapse into a single box, silently dropping one real call path from the
// picture (confirmed: `dot` renders two `"handle" -> "Target"` edges as one
// node, one edge). This assigns each drawn node a graphviz id unique per
// (name, filePath): a name that occurs in only one file keeps name-as-id (so
// graphs with no collision render byte-for-byte as before), while a name shared
// across files gets a file-qualified id AND a label with the filename appended
// so the two boxes read as distinct on the page. The '@@' id separator is
// arbitrary and never shown (a colliding node always carries an explicit
// label); NUL is deliberately NOT used as the separator — graphviz is C and
// would truncate the id at the NUL, silently reintroducing the very merge this
// fixes (and a stray NUL once made git treat this file as binary).
function nodeIdentities(nodes) {
  const filesByName = new Map();
  for (const n of nodes) {
    if (!filesByName.has(n.name)) filesByName.set(n.name, new Set());
    filesByName.get(n.name).add(n.filePath);
  }
  const collides = name => (filesByName.get(name)?.size || 0) > 1;
  const idOf = n => (collides(n.name) ? `${n.name}@@${n.filePath}` : n.name);
  // null for a non-colliding node → no explicit label needed (graphviz defaults
  // the label to the id, which is just the name). For a collision, the label is
  // the name plus the file's basename so two same-named boxes are told apart.
  const labelOf = n => {
    if (!collides(n.name)) return null;
    const base = String(n.filePath).split(/[\\/]/).filter(Boolean).pop() || String(n.filePath);
    return { name: n.name, base };
  };
  return { idOf, labelOf };
}

function buildDot(symbol, callers = [], callees = [], { maxRender, transitiveEdges = [], tooltips = false } = {}) {
  const esc = s => String(s).replace(/"/g, '\\"');
  const dedupedCallers = dedupeNodes(callers);
  const dedupedCallees = dedupeNodes(callees);
  const dedupedTransitive = dedupeEdges(transitiveEdges);
  const [callerBudget, calleeBudget, transitiveBudget] = allocateRenderBudget(
    maxRender, [dedupedCallers.length, dedupedCallees.length, dedupedTransitive.length]
  );
  const drawnCallers = dedupedCallers.slice(0, callerBudget);
  const drawnCallees = dedupedCallees.slice(0, calleeBudget);
  const drawnTransitive = dedupedTransitive.slice(0, transitiveBudget);

  // The queried symbol keeps a stable, un-disambiguated id — it's the one node
  // the user named, so its identity is never in question — but it still joins
  // the collision set below so that a caller/callee sharing its name is pushed
  // onto a distinct id instead of collapsing into the root as a bogus self-loop.
  const rootId = esc(symbol);
  const { idOf, labelOf } = nodeIdentities([
    { name: symbol, filePath: null },
    ...drawnCallers, ...drawnCallees,
    ...drawnTransitive.flatMap(e => [e.from, e.to]),
  ]);

  // Style goals: calm and documentation-grade, not playful. Straight (polyline)
  // edges instead of graphviz's default curved splines, a muted slate palette,
  // and a solid white background (NOT transparent — these images get committed
  // into TECHNICAL.md and viewed on GitHub in both light and dark mode; dark
  // slate text on a transparent ground would be invisible in dark mode).
  const lines = [
    'digraph callgraph {',
    '  rankdir=LR; bgcolor="white"; splines=polyline; nodesep=0.35; ranksep=0.75; pad=0.2;',
    '  node [shape=box, style="rounded,filled", fillcolor="#f8fafc", color="#cbd5e1", fontcolor="#334155", fontname="Helvetica", fontsize=11, penwidth=1.1, margin="0.20,0.11"];',
    '  edge [color="#94a3b8", arrowsize=0.6, penwidth=1.0];',
    `  "${rootId}" [fillcolor="#e2e8f0", color="#94a3b8", fontcolor="#0f172a", fontname="Helvetica-Bold", penwidth=1.5];`,
  ];

  // Explicit node declarations, emitted once per id, for nodes that need
  // attributes beyond the graph defaults: a disambiguating label (colliding
  // names) and/or a file-path tooltip (svg output). The queried symbol is
  // excluded — its declaration above is authoritative, and buildDot isn't
  // passed the root's own filePath to tooltip it with. When neither applies
  // (a non-colliding graph with tooltips off), no lines are added here, so the
  // output stays byte-for-byte identical to before either feature existed.
  const declared = new Set([rootId]);
  for (const n of [...drawnCallers, ...drawnCallees, ...drawnTransitive.flatMap(e => [e.from, e.to])]) {
    const id = esc(idOf(n));
    if (declared.has(id)) continue;
    declared.add(id);
    const attrs = [];
    const lab = labelOf(n);
    if (lab) attrs.push(`label="${esc(lab.name)}\\n(${esc(lab.base)})"`);
    // In svg output graphviz wraps a tooltip'd node in <a xlink:title="...">,
    // so hovering a box reveals which file the symbol lives in — the
    // disambiguation a same-basename collision can't show, without cluttering
    // the label. graphviz ignores tooltip for raster formats, so main only
    // sets tooltips for svg-family output to keep png's DOT lean.
    if (tooltips && n.filePath) attrs.push(`tooltip="${esc(String(n.filePath))}"`);
    if (attrs.length) lines.push(`  "${id}" [${attrs.join(', ')}];`);
  }

  for (const c of drawnCallers) {
    const attrs = edgeStyleAttrs(c);
    const style = attrs.length ? ` [${attrs.join(', ')}]` : '';
    lines.push(`  "${esc(idOf(c))}" -> "${rootId}"${style};`);
  }
  for (const c of drawnCallees) {
    // callees never get the dashed "test" treatment (a production symbol
    // calling into test code is unusual and not what that styling is for),
    // but a file-kind callee still deserves the same "not a real call" look.
    const attrs = c.kind === 'file' ? edgeStyleAttrs(c) : [];
    const style = attrs.length ? ` [${attrs.join(', ')}]` : '';
    lines.push(`  "${rootId}" -> "${esc(idOf(c))}"${style};`);
  }
  for (const edge of drawnTransitive) {
    const attrs = edgeStyleAttrs(edge.from, depthColor(edge.depth));
    lines.push(`  "${esc(idOf(edge.from))}" -> "${esc(idOf(edge.to))}" [${attrs.join(', ')}];`);
  }

  lines.push('}');
  return lines.join('\n');
}

function renderTruncationNote(kind, distinctCount, maxRender) {
  if (!Number.isFinite(maxRender) || distinctCount <= maxRender) return null;
  return `codeshot: rendering ${maxRender} of ${distinctCount} distinct ${kind} — image capped by --max-render; rerun with a larger --max-render (or omit it) to see the rest.`;
}

// dot has no concept of inferring format from a file's extension — it only
// ever writes whatever -T<format> says, silently, regardless of --out's name.
// `--out diagram.svg` with no --format thus writes real PNG bytes into a
// .svg-named file with no error or hint. Only fires for extensions dot itself
// recognizes as an output format, to avoid false positives on e.g. `--out notes.dot.bak`.
const KNOWN_DOT_FORMATS = new Set(['png', 'svg', 'svgz', 'pdf', 'jpg', 'jpeg', 'gif', 'webp', 'ps', 'eps', 'json', 'dot', 'xdot']);
function formatMismatchWarning(outFile, format) {
  if (!outFile) return null;
  const ext = String(outFile).split('.').pop().toLowerCase();
  if (!KNOWN_DOT_FORMATS.has(ext) || ext === format.toLowerCase()) return null;
  return `codeshot: --out ends in '.${ext}' but --format is '${format}' — the file will contain ${format} data despite its name; pass --format ${ext} to match, or rename --out.`;
}

function depthBudgetWarning(truncated, budget) {
  if (!truncated) return null;
  return `codeshot: --depth traversal stopped early (safety cap of ${budget} discovered nodes) — the graph beyond this point is incomplete; rerun with a smaller --depth or --limit, a more specific symbol, or a higher --max-depth-nodes to raise the cap.`;
}

// codegraph's callers/callees fuzzy-match a partial/inexact query (e.g. `New`
// resolving to `NewOracle`) but their JSON response's "symbol" field just
// echoes the raw query back — nothing in that contract reveals the resolved
// canonical name. `query --json` is a separate, stable, documented contract
// that does resolve it, and returns `[]` (not codegraph's plain-text "not
// found" message) when nothing matches — so this doubles as a clean
// existence check before any further codegraph calls.
async function resolveSymbol(query, repoPath) {
  const results = await runCodegraph(['query', '--path', repoPath, '--json', '--limit', '1', '--', query]);
  if (!Array.isArray(results) || results.length === 0) {
    console.error(`codeshot: symbol '${query}' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.`);
    process.exit(1);
  }
  return results[0].node.name;
}

async function collectTransitive(direction, repoPath, limit, maxDepth, seedNodes, discovered, budget) {
  const edges = [];
  let frontier = seedNodes;
  let truncated = discovered.size >= budget;
  for (let hop = 2; hop <= maxDepth && frontier.length > 0 && !truncated; hop++) {
    const nextFrontier = [];
    for (const node of frontier) {
      let results;
      try {
        ({ [direction]: results } = await runCodegraph([direction, '--path', repoPath, '--limit', String(limit), '--json', '--', node.name]));
      } catch {
        continue; // one node's callers/callees query failing (e.g. an ambiguous name) shouldn't abort the whole traversal
      }
      for (const r of dedupeNodes(results || [])) {
        const rKey = `${r.name} ${r.filePath}`;
        edges.push(direction === 'callers' ? { from: r, to: node, depth: hop } : { from: node, to: r, depth: hop });
        if (discovered.has(rKey)) continue;
        if (discovered.size >= budget) { truncated = true; continue; }
        discovered.add(rKey);
        nextFrontier.push(r);
      }
    }
    frontier = nextFrontier;
  }
  return { edges, truncated };
}

// --- --architecture mode: whole-repo file-level dependency graph ---------

// `query ""` results come back as `{ node: {...}, score }`, unlike
// callers/callees' flat `{ name, kind, filePath }` shape — this just unwraps
// that envelope. A "kind":"file" entry (the file itself, not a function) is
// KEPT, not dropped: `codegraph callees <fileBasename>` is a real, working
// query against it (verified against a live index), and it's the only way to
// see calls made from inside a top-level anonymous callback — codegraph
// attributes those to the enclosing file node, since no named function
// contains them (e.g. this repo's own test/run.js: every assertion inside a
// `test('...', () => { ... })` body calls into render/callgraph.js this way,
// and none of those calls are reachable from any named symbol codeshot could
// otherwise probe). Without this, --architecture mode is structurally blind
// to that whole category of cross-file call. Also drops any entry with no
// usable `name` (a malformed/partial index record) — probeFileEdges passes
// this straight into a `codegraph` subprocess's argv, where `undefined` would
// throw before codegraph ever gets to report its own "not found", bypassing
// the fatal:false resilience that's supposed to let one bad entry skip past
// without aborting the whole scan.
function unwrapQueryNodes(queryResults) {
  return (queryResults || [])
    .map(r => r.node)
    .filter(n => n && typeof n.name === 'string' && n.name.length > 0);
}

function symbolBudgetWarning(truncated, budget) {
  if (!truncated) return null;
  // Names the SHAPE of the incompleteness, not just its existence: the kept
  // subset is a path-sorted prefix (see sortSymbolsForEnumeration), so the
  // missing symbols are not a random sample — they are the last files by path,
  // and since only scanned symbols contribute outgoing edges, those files can
  // render as sinks that appear to call nothing. "Incomplete" alone reads as
  // "a few edges missing" and would leave that misreading in place.
  return `codeshot: --architecture stopped enumerating after ${budget} symbols (--max-symbols) — the graph is incomplete; rerun with a larger --max-symbols to cover the rest of the repo. Note the cut is a path-sorted prefix, not a sample: files later in path order went unprobed, so they may appear to call nothing when they do.`;
}

// With zero cross-file edges, buildArchitectureDot emits a graph with no nodes —
// a blank image. Node count alone can't tell that apart from a legitimately
// edge-free repo, so warn with the likely cause (small/single-file repo, or an
// index that was never built) instead of writing a silent blank picture. Pure:
// takes the aggregated file edges, returns the warning string or null.
function emptyArchitectureWarning(fileEdges) {
  if (Array.isArray(fileEdges) && fileEdges.length > 0) return null;
  return `codeshot: --architecture found no cross-file call edges — the diagram is blank. codegraph's index has no resolved calls between files in this repo (it may be small or single-file, or the index may be missing — run 'codegraph init <path>' to build it, then 'codegraph status' to confirm).`;
}

// The set of names that appear in more than one distinct FILE. Shared by
// probeFileEdges (which re-probes exactly these, file-qualified) and by
// duplicateNameWarning, so the fix and the warning can never disagree about
// what counts as a duplicate.
//
// Counting distinct files, not symbol occurrences, is load-bearing: two symbols
// sharing a name inside ONE file (Go's `String()` on two types in one file, two
// class methods, overloads) have no file-attribution ambiguity at all — the
// bare-name probe's union of their callees is already exactly right, and
// routing them through the file-qualified probe would only lose edges. Pure.
function duplicateNames(symbols) {
  const files = new Map();
  for (const s of symbols || []) {
    if (!files.has(s.name)) files.set(s.name, new Set());
    files.get(s.name).add(s.filePath);
  }
  return new Set([...files.entries()].filter(([, f]) => f.size > 1).map(([name]) => name));
}

// Parses the trail that `codegraph node -f <file> <name>` prints at the end of
// its output, into file-qualified callees. This is the ONLY file-disambiguated
// callee probe codegraph offers — `codegraph callees` takes a bare name with no
// --file flag — so it's how --architecture resolves same-named symbols in
// different files instead of guessing.
//
// Returns null whenever the response can't be trusted to be a COMPLETE call list
// for exactly the symbol in `expectedFile`, so the caller falls back to the
// bare-name probe rather than silently under-reporting. Four ways it bails, each
// a real measured behavior of codegraph 1.5.0, not defensive padding:
//
//  1. No trail section. File-mode output (which `-f <file> <basename>` returns)
//     has no trail at all, and neither does an unrecognized/changed format.
//     A symbol that genuinely calls nothing still prints the trail header, so
//     this cleanly separates "no calls" ([]) from "no answer" (null).
//  2. More than one trail section. codegraph concatenates every match into one
//     response, so a multi-block answer means the probe was still ambiguous;
//     keeping just one block would silently drop the others' edges.
//  3. The trail is TRUNCATED. codegraph caps the line at 12 entries and appends
//     `+N more` (measured: `main` in this repo has 23 callees, the trail shows
//     12). The trail is a human-facing summary, not an API — taking the visible
//     12 would trade this fix's fabricated edges for missing ones, which
//     TECHNICAL.md's own limitations call the worse failure ("a *missing* edge
//     is the invisible version and harder to catch").
//  4. The reported location isn't in `expectedFile`. `-f` is a PREFERENCE, not a
//     filter — measured: `node -f test/run.js -- buildDot` and even
//     `-f no/such/file.js -- buildDot` both happily answer with
//     render/callgraph.js's buildDot, exit 0. Without this check the "fix" would
//     confidently attribute another file's edges and never fall back.
//
// Reading text rather than JSON is the acknowledged cost (`codegraph node` has
// no --json). Scanning only AFTER the trail marker is the other guard: the
// response embeds the symbol's own source, which in this repo can itself contain
// lines that look like a trail. Pure.
function parseNodeCalls(out, expectedFile) {
  const text = String(out || '');
  const trailAt = text.indexOf('**Trail');
  if (trailAt === -1) return null;
  if (text.indexOf('**Trail', trailAt + 1) !== -1) return null;
  const location = text.match(/^\*\*Location:\*\*\s*(.+?):(\d+)\s*$/m);
  if (!location) return null;
  if (expectedFile !== undefined && location[1].trim() !== expectedFile) return null;
  const tail = text.slice(trailAt);
  const line = tail.match(/^\*\*Calls\s*→\*\*\s*(.+)$/m);
  if (!line) return [];
  if (/\+\d+\s+more/.test(line[1])) return null;
  const calls = [];
  const entry = /([^\s,()]+)\s+\(([^()]+):(\d+)\)/g;
  let m;
  while ((m = entry.exec(line[1])) !== null) {
    const [, name, filePath] = m;
    // The trail carries no `kind`, so the file-node filter probeFileEdges applies
    // to the JSON path has to be structural here: codegraph renders a file node as
    // its own basename (e.g. `run.js (test/run.js:1)`). Counting one as a real call
    // would fabricate exactly the full-weight edge that keeping file nodes out of
    // aggregateFileEdges exists to prevent.
    if (name === path.basename(filePath)) continue;
    calls.push({ name, filePath });
  }
  return calls;
}

// codegraph's callers/callees take a bare name with no --file disambiguation, so
// two same-named symbols in different files are ambiguous to a bare-name probe —
// a real risk at --architecture's scale (probing hundreds of names), not a corner
// case. probeFileEdges now resolves that for ordinary symbols via `node -f`, so
// this reports the fix for those and warns only about the residue it still can't
// disambiguate: file nodes, which unwrapQueryNodes deliberately keeps in the
// probed set and which `node -f` answers in a different (file-mode) shape.
function duplicateNameWarning(symbols) {
  const dupes = duplicateNames(symbols);
  if (!dupes.size) return null;
  const fileDupes = [...new Set((symbols || [])
    .filter(s => s.kind === 'file' && dupes.has(s.name))
    .map(s => s.name))];
  const resolved = [...dupes].filter(n => !fileDupes.includes(n));
  const parts = [];
  if (resolved.length) {
    parts.push(`${resolved.length} symbol name(s) appear in more than one file (e.g. ${resolved.slice(0, 3).join(', ')}) — codeshot re-probes these with a file-qualified 'codegraph node -f' so their edges land on the right file, falling back to the bare name (which over-reports rather than under-reports) where that probe can't answer completely.`);
  }
  if (fileDupes.length) {
    parts.push(`${fileDupes.length} file name(s) appear in more than one directory (e.g. ${fileDupes.slice(0, 3).join(', ')}) — these are still probed by bare name, so their edges may be attributed to the wrong file.`);
  }
  return `codeshot: ${parts.join(' ')}`;
}

// Drops self-file edges (intra-file calls aren't cross-module architecture)
// and any edge missing a real from/to filePath (an unresolved external or
// stdlib callee has no file of its own and would otherwise render as a
// bogus "" node).
function aggregateFileEdges(symbolEdges) {
  const weights = new Map();
  for (const { fromFile, toFile } of symbolEdges || []) {
    if (!fromFile || !toFile || fromFile === toFile) continue;
    const key = `${fromFile} -> ${toFile}`;
    weights.set(key, (weights.get(key) || { from: fromFile, to: toFile, weight: 0 }));
    weights.get(key).weight += 1;
  }
  return [...weights.values()];
}

// The group a file rolls up into at --group-depth <n>: its first `n` DIRECTORY
// segments, with a trailing slash so a group reads as a directory rather than a
// file. A file with no directory at all (a repo-root file) has nothing to roll
// into and stays itself — grouping it under "" would invent a nameless node and
// silently merge every root file into one box. A file shallower than `n` keeps
// whatever directories it has, so a deeper --group-depth degrades to per-file
// output rather than erroring. Pure; posix and Windows separators both split.
function groupPath(filePath, depth) {
  const parts = String(filePath || '').split(/[\\/]/).filter(Boolean);
  const dirs = parts.slice(0, -1);
  if (!dirs.length) return filePath;
  return `${dirs.slice(0, depth).join('/')}/`;
}

// Rewrites file-level edges as group-level edges, summing the weights of every
// file pair that collapses into the same group pair. Self-group edges are
// dropped for exactly the reason aggregateFileEdges drops self-file ones: once
// a directory is the unit, a call between two files inside it is intra-module
// structure, not the cross-module coupling this diagram is about. Returns the
// same {from, to, weight} shape, so everything downstream (topFilesByWeight,
// emptyArchitectureWarning, isTestRef's dashed test nodes, buildArchitectureDot)
// works on groups unchanged. Pure; `depth` unset returns the input untouched.
function rollupFileEdges(fileEdges, depth) {
  if (!Number.isFinite(depth)) return fileEdges;
  const weights = new Map();
  for (const e of fileEdges || []) {
    const from = groupPath(e.from, depth);
    const to = groupPath(e.to, depth);
    if (from === to) continue;
    const key = `${from} -> ${to}`;
    weights.set(key, (weights.get(key) || { from, to, weight: 0 }));
    weights.get(key).weight += e.weight;
  }
  return [...weights.values()];
}

// A rollup that eats every edge yields a blank image whose cause is the flag,
// not the code — emptyArchitectureWarning would blame a missing index instead.
//
// `groupCount` (how many distinct groups the pre-rollup endpoints mapped to)
// separates the two genuinely different causes, which want opposite advice:
// one group means the depth is too coarse and a deeper --group-depth will help;
// several groups means every call is intra-module at this depth, the diagram is
// a real (if boring) finding, and going deeper will keep returning blank. The
// earlier single-message version asserted "within one directory" in both cases,
// which contradicted the user's own tree and sent them down a dead end.
// Pure: takes the edge counts either side of the rollup, returns the string or null.
function groupCollapseWarning(beforeCount, afterCount, depth, groupCount) {
  if (!Number.isFinite(depth) || beforeCount === 0 || afterCount > 0) return null;
  const advice = groupCount > 1
    ? `the ${groupCount} groups at this depth have no calls between them, so there is genuinely no cross-module coupling to draw — a deeper --group-depth will stay blank; drop the flag for the per-file graph.`
    : 'every file falls into a single group at this depth. Try a deeper --group-depth, or drop the flag for the per-file graph.';
  return `codeshot: --group-depth ${depth} left no edges to draw — all ${beforeCount} cross-file edge(s) collapsed within a group: ${advice}`;
}

// Top-N files by total in+out edge weight — simpler than a connected-
// component/centrality algorithm, consistent with keeping v1 minimal.
// `null` means "no cap" (mirrors allocateRenderBudget's no-op case).
function topFilesByWeight(fileEdges, maxRender) {
  if (!Number.isFinite(maxRender)) return null;
  const weight = new Map();
  for (const e of fileEdges) {
    weight.set(e.from, (weight.get(e.from) || 0) + e.weight);
    weight.set(e.to, (weight.get(e.to) || 0) + e.weight);
  }
  const ranked = [...weight.entries()].sort((a, b) => b[1] - a[1]).slice(0, maxRender);
  return new Set(ranked.map(([file]) => file));
}

function buildArchitectureDot(fileEdges, { maxRender } = {}) {
  const esc = s => String(s).replace(/"/g, '\\"');
  const keep = topFilesByWeight(fileEdges, maxRender);
  const kept = keep ? fileEdges.filter(e => keep.has(e.from) && keep.has(e.to)) : fileEdges;

  const files = new Set();
  for (const e of kept) { files.add(e.from); files.add(e.to); }

  // Same muted, documentation-grade house style as buildDot (see its comment) —
  // this diagram gets committed/viewed the same way, so it should look like
  // part of the same tool, not a differently-themed second renderer.
  const lines = [
    'digraph architecture {',
    '  rankdir=LR; bgcolor="white"; splines=polyline; nodesep=0.35; ranksep=0.75; pad=0.2;',
    '  node [shape=box, style="rounded,filled", fillcolor="#f8fafc", color="#cbd5e1", fontcolor="#334155", fontname="Helvetica", fontsize=11, penwidth=1.1, margin="0.20,0.11"];',
    '  edge [color="#94a3b8", arrowsize=0.6, penwidth=1.0];',
  ];
  for (const file of files) {
    const attrs = isTestRef({ name: '', filePath: file }) ? ' [style="rounded,filled,dashed"]' : '';
    lines.push(`  "${esc(file)}"${attrs};`);
  }
  for (const e of kept) {
    lines.push(`  "${esc(e.from)}" -> "${esc(e.to)}" [label="${e.weight}"];`);
  }
  lines.push('}');
  return lines.join('\n');
}

function architectureOutputBaseName(repoPath) {
  return sanitizeForFilename(path.basename(path.resolve(repoPath)));
}

// Enumerates (almost) every symbol in the index. An empty-string `query`
// WITHOUT --limit silently caps around 50 results regardless of repo size
// (confirmed on a real 1,870-node index) — but a sufficiently large --limit
// (confirmed with 500 and 2000 against the same index) returns every result
// codegraph has, ignoring the requested number rather than capping at it.
// So a big fixed --limit is passed here purely to push codegraph into its
// "return everything" behavior; the real cap enforced is the client-side
// slice to maxSymbols below, exactly as intended.
const ENUMERATION_QUERY_LIMIT = 100000;

// The untruncated, sorted symbol list — split out from enumerateSymbols so
// --diff mode (below) can filter the FULL index down to a handful of known
// changed files without first losing symbols to --max-symbols' repo-wide
// cap, which exists to bound --architecture's expensive per-symbol probing,
// not this cheap single enumeration query.
async function enumerateAllSymbols(repoPath) {
  const results = await runCodegraph(['query', '--path', repoPath, '--json', '--limit', String(ENUMERATION_QUERY_LIMIT), '--', '']);
  return sortSymbolsForEnumeration(unwrapQueryNodes(results));
}

async function enumerateSymbols(repoPath, maxSymbols) {
  const symbols = await enumerateAllSymbols(repoPath);
  const truncated = symbols.length > maxSymbols;
  return { symbols: symbols.slice(0, maxSymbols), truncated };
}

// codegraph's order for the enumeration query is unspecified (untested whether
// it is insertion, alphabetical, or id order — see TECHNICAL.md), so on a repo
// larger than --max-symbols the slice above would keep a DIFFERENT subset run to
// run: the same repo, unchanged, could yield a different diagram each time, and
// a committed diagram guarded by --check could flap in CI for no code reason.
// Sorting by (filePath, name) makes the kept subset a deterministic function of
// the index alone. It does not make the subset representative — it is still a
// prefix, now explicitly a path-ordered one, so a truncated scan covers the
// code-point-first files rather than an even sample (symbolBudgetWarning says
// so). Pure.
//
// Compared by code point, deliberately NOT String#localeCompare: localeCompare
// with no explicit locale uses the *implementation-default* locale, which varies
// with the environment and with how the Node binary's ICU was built — so it
// would reintroduce, one layer down, exactly the run-to-run variability this
// function exists to remove (a dev box and a CI runner could keep different
// subsets of the same repo, and --check would fail with no code change). Code
// point order is machine-independent by construction. Its only cost is that
// 'Api.js' sorts before 'api.js'; nothing here needs human-facing collation.
function sortSymbolsForEnumeration(symbols) {
  const byCodePoint = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
  return [...(symbols || [])].sort((a, b) =>
    byCodePoint(String(a.filePath || ''), String(b.filePath || '')) ||
    byCodePoint(String(a.name || ''), String(b.name || '')));
}

// The file-qualified probe, used only for names that are actually ambiguous.
// Returns null whenever the answer can't be trusted to be this file's complete
// call list — see parseNodeCalls for the four cases — so the caller falls back
// to the bare-name probe. The fallback is over-inclusive (it's the union across
// same-named symbols, the very thing this fix narrows) but never under-inclusive,
// which is the right way round to fail.
async function probeCallsInFile(symbol, repoPath) {
  const out = await runCodegraph(
    ['node', '--path', repoPath, '-f', symbol.filePath, '--', symbol.name],
    { fatal: false, json: false }
  );
  return out === null ? null : parseNodeCalls(out, symbol.filePath);
}

// Sequential — same concurrency hazard as collectTransitive: parallel
// codegraph calls against one index race on its schema_versions table.
// fatal:false + the null checks below are what let one ambiguous/not-found
// probed name (real and expected at this scale — see duplicateNameWarning)
// skip past without aborting the whole multi-minute scan.
//
// Duplicate-named symbols take the file-qualified `node -f` route; everything
// else keeps the cheaper bare-name `callees --json` route. That split is
// deliberate: `node -f` returns the symbol's full source on every call, which is
// only affordable because duplicates are a small slice of a repo (~2% measured
// on a real ~1,900-node Go index), and it keeps the text-parsing path off 98% of
// the scan.
async function probeFileEdges(symbols, repoPath, limit) {
  const edges = [];
  const dupes = duplicateNames(symbols);
  for (let i = 0; i < symbols.length; i++) {
    const s = symbols[i];
    // File nodes are excluded: `node -f` answers those in file mode, a different
    // output shape parseNodeCalls deliberately rejects.
    let callees = dupes.has(s.name) && s.kind !== 'file' && s.filePath
      ? await probeCallsInFile(s, repoPath)
      : null;
    if (callees === null) {
      const result = await runCodegraph(
        ['callees', '--path', repoPath, '--limit', String(limit), '--json', '--', s.name],
        { fatal: false }
      );
      if (result === null) continue;
      // A "kind":"file" callee is a module-level/import reference codegraph
      // couldn't resolve to a real call site — symbol mode already treats
      // these as unverified (edgeStyleAttrs draws them dotted/gray, not a
      // real call edge); counting one as a full-weight file-to-file edge
      // here would fabricate exactly the kind of edge this file-node-probing
      // change exists to stop fabricating.
      callees = (result.callees || []).filter(c => c.kind !== 'file');
    }
    for (const c of callees) {
      edges.push({ fromFile: s.filePath, toFile: c.filePath });
    }
    if ((i + 1) % 25 === 0) {
      console.error(`codeshot: scanned ${i + 1}/${symbols.length} symbols...`);
    }
  }
  return edges;
}

async function runArchitectureMode(repoPath, { limit, maxSymbols, maxRender, groupDepth }) {
  const { symbols, truncated } = await enumerateSymbols(repoPath, maxSymbols);
  const symbolWarning = symbolBudgetWarning(truncated, maxSymbols);
  if (symbolWarning) console.error(symbolWarning);
  const dupeWarning = duplicateNameWarning(symbols);
  if (dupeWarning) console.error(dupeWarning);

  const symbolEdges = await probeFileEdges(symbols, repoPath, limit);
  const rawFileEdges = aggregateFileEdges(symbolEdges);
  // The rollup happens here, on aggregated edges, rather than by rewriting
  // filePaths at probe time: probing must stay file-exact (parseNodeCalls'
  // expectedFile check, isTestRef, the duplicate-name attribution all key off
  // the real path), and collapsing afterwards keeps --group-depth a pure,
  // testable view over the same data instead of a second scan mode.
  const fileEdges = rollupFileEdges(rawFileEdges, groupDepth);

  const groupCount = Number.isFinite(groupDepth)
    ? new Set(rawFileEdges.flatMap(e => [groupPath(e.from, groupDepth), groupPath(e.to, groupDepth)])).size
    : 0;
  const collapseWarning = groupCollapseWarning(rawFileEdges.length, fileEdges.length, groupDepth, groupCount);
  if (collapseWarning) console.error(collapseWarning);
  const emptyWarning = emptyArchitectureWarning(fileEdges);
  if (emptyWarning && !collapseWarning) console.error(emptyWarning);

  // Counted (and capped) after the rollup: --max-render bounds what is actually
  // drawn, and with --group-depth the drawn nodes are groups, not files.
  const totalFiles = new Set(fileEdges.flatMap(e => [e.from, e.to])).size;
  const note = renderTruncationNote(groupDepth ? 'groups' : 'files', totalFiles, maxRender);
  if (note) console.error(note);

  return buildArchitectureDot(fileEdges, { maxRender });
}

// --- --diff mode: call graph scoped to a set of changed files ------------

// Shells out to `git diff --name-only`. With no explicit ref, this diffs
// against HEAD (not git's own bare-`git diff` default, which is working-tree
// vs the INDEX and misses fully-staged changes — e.g. right after `git add
// -A`, plain `git diff` reports nothing at all). Passing 'HEAD' explicitly
// covers staged + unstaged in one comparison, matching what the docs promise
// ("working tree vs HEAD") and the user's "what have I changed" mental
// model. --diff-ref overrides this with a specific range/ref — the form
// that makes this reproducible for --embed --check in CI, where there is no
// working tree to diff against.
function gitDiffFiles(repoPath, ref) {
  // --end-of-options: without it, a --diff-ref value starting with '-' (e.g.
  // untrusted input from a CI template) is parsed by git as a FLAG instead
  // of a revision — the same argv-injection risk resolveSymbol's '--' guards
  // against for codegraph, just with git's own equivalent (git predates
  // universal '--' pathspec-boundary support with this flag specifically for
  // disambiguating an option-like revision argument; unlike '--', it doesn't
  // also mark what follows as a pathspec, so `ref` is still parsed as a
  // normal revision/range). -z: NUL-terminated, UNQUOTED output — sidesteps
  // core.quotePath's C-style octal-escaping of non-ASCII filenames (the
  // default), which the earlier line-based '\n'.split would have silently
  // left mangled (and un-matchable against codegraph's raw filePath) rather
  // than decoded.
  const args = ['diff', '--name-only', '-z', '--end-of-options', ref || 'HEAD'];
  let out;
  try {
    out = execFileSync('git', args, { cwd: repoPath, encoding: 'utf8' });
  } catch (err) {
    console.error(`codeshot: 'git ${args.join(' ')}' failed in '${repoPath}' — confirm it's a git repo${ref ? ` and '${ref}' is a valid ref/range` : ''}. (${String(err.message).split('\n')[0]})`);
    process.exit(1);
  }
  // git's diff output always uses forward slashes internally regardless of
  // platform, so no separator normalization is needed on this side (unlike
  // matchRootSymbols' defensive normalization of codegraph's OWN filePath).
  return out.split('\0').map(l => l.trim()).filter(Boolean);
}

// Pure: which enumerated symbols live in one of the changed files. Split out
// from runDiffMode so the matching logic — the same path-format risk PR #24
// fixed for --architecture's file attribution — is unit-testable without
// shelling out to git or codegraph.
function matchRootSymbols(symbols, changedFiles) {
  const changedSet = new Set((changedFiles || []).map(f => String(f).replace(/\\/g, '/')));
  return (symbols || []).filter(s => s.filePath && changedSet.has(String(s.filePath).replace(/\\/g, '/')));
}

function diffNoChangesWarning(diffRef) {
  return `codeshot: --diff found no changed files${diffRef ? ` for '${diffRef}'` : ' (working tree matches HEAD)'} — nothing to diagram.`;
}

// A zero-changed-files diff is a common, unremarkable state for --diff
// specifically (a clean working tree, unlike --architecture's empty-graph
// case which signals something's actually wrong) — so unlike
// emptyArchitectureWarning, this refuses rather than warns-and-proceeds
// when --embed is involved: rendering the blank graph anyway would silently
// overwrite a real, previously-committed diagram and doc block at exit 0,
// the kind of quiet data loss a script or pre-commit hook could easily miss.
function diffEmbedRefusal(diffRef, embedFile) {
  return `codeshot: --diff found no changed files${diffRef ? ` for '${diffRef}'` : ' (working tree matches HEAD)'} — refusing to overwrite the existing diagram embedded in '${embedFile}' with a blank one. Pass --diff-ref to target a specific range, or drop --embed to render a (blank) image on its own.`;
}

// Unlike emptyGraphWarning (one queried symbol, so one warning reads
// naturally), --diff can have many roots — a per-root warning would be
// noise on a large diff, so this reports the aggregate count instead of
// naming each one.
function diffEmptyRootsWarning(emptyCount, totalCount) {
  if (emptyCount === 0) return null;
  return `codeshot: ${emptyCount} of ${totalCount} changed symbol(s) have no callers or callees in codegraph's index — drawn as lone boxes. They may be unused (dead code) or entry points, or codegraph's index may be incomplete for their file.`;
}

function diffNoSymbolsWarning(repoPath, changedCount) {
  return `codeshot: --diff found ${changedCount} changed file(s) but no matching symbols in codegraph's index — they may not define top-level symbols, may be in a language codegraph doesn't index, or the index may be stale (run 'codegraph sync ${repoPath}').`;
}

// Mirrors symbolBudgetWarning's "warn, name the shape of the cut" stance for
// --architecture, but for --diff's much smaller and differently-ordered
// budget: how many of the CHANGED, matched symbols get probed for
// callers/callees (two codegraph calls each), not how many of the whole
// repo get enumerated.
function diffSymbolBudgetWarning(matchedCount, budget) {
  if (matchedCount <= budget) return null;
  return `codeshot: --diff matched ${matchedCount} changed symbols but only probing the first ${budget} (--max-symbols) — the diagram is incomplete; rerun with a larger --max-symbols to cover the rest of the diff.`;
}

// Multi-root variant of buildDot: instead of one queried symbol at the
// center, every root (a symbol defined in a changed file) is drawn bold/
// highlighted, same visual weight buildDot gives its single root, and its
// direct callers/callees fan out around it in the house style. Unlike
// buildDot, roots are always drawn in full (they ARE the diff) — --max-render
// bounds only the callers/callees pulled in around them, the same "budget the
// discovered context, not the thing asked for" stance --depth's node budget
// takes for symbol mode.
function buildDiffDot(roots, edges, { maxRender, tooltips = false } = {}) {
  const esc = s => String(s).replace(/"/g, '\\"');
  const keyOf = n => `${n.name} ${n.filePath}`;
  const rootKeys = new Set(roots.map(keyOf));
  const dedupedEdges = dedupeEdges(edges);

  const allNodes = dedupeNodes([...roots, ...dedupedEdges.flatMap(e => [e.from, e.to])]);
  const nonRootNodes = allNodes.filter(n => !rootKeys.has(keyOf(n)));
  const keepNonRoot = Number.isFinite(maxRender)
    ? new Set(nonRootNodes.slice(0, maxRender).map(keyOf))
    : null;
  const keep = key => rootKeys.has(key) || !keepNonRoot || keepNonRoot.has(key);

  const drawnNodes = allNodes.filter(n => keep(keyOf(n)));
  const drawnEdges = dedupedEdges.filter(e => keep(keyOf(e.from)) && keep(keyOf(e.to)));

  const { idOf, labelOf } = nodeIdentities(drawnNodes);

  const lines = [
    'digraph callgraph {',
    '  rankdir=LR; bgcolor="white"; splines=polyline; nodesep=0.35; ranksep=0.75; pad=0.2;',
    '  node [shape=box, style="rounded,filled", fillcolor="#f8fafc", color="#cbd5e1", fontcolor="#334155", fontname="Helvetica", fontsize=11, penwidth=1.1, margin="0.20,0.11"];',
    '  edge [color="#94a3b8", arrowsize=0.6, penwidth=1.0];',
  ];
  for (const n of drawnNodes) {
    const id = esc(idOf(n));
    const attrs = [];
    const lab = labelOf(n);
    if (lab) attrs.push(`label="${esc(lab.name)}\\n(${esc(lab.base)})"`);
    if (tooltips && n.filePath) attrs.push(`tooltip="${esc(String(n.filePath))}"`);
    if (rootKeys.has(keyOf(n))) attrs.push('fillcolor="#e2e8f0"', 'color="#94a3b8"', 'fontcolor="#0f172a"', 'fontname="Helvetica-Bold"', 'penwidth=1.5');
    lines.push(`  "${id}"${attrs.length ? ` [${attrs.join(', ')}]` : ''};`);
  }
  for (const e of drawnEdges) {
    // Deliberately does NOT replicate buildDot's caller-vs-callee asymmetry
    // (there, a callee edge is never dashed just because the single queried
    // root happens to live in a test file — see buildDot's comment). That
    // asymmetry doesn't transfer here: with multiple roots, a root-to-root
    // edge is simultaneously "root A's callee edge" and "root B's caller
    // edge" depending only on which of the two probes discovered it first,
    // an arbitrary artifact of sortSymbolsForEnumeration's probe order that
    // dedupeEdges' from/to-only key can't see. Styling off `edge.kind` (an
    // earlier version of this function) made the dashed "test" indicator
    // flip on and off for the identical call depending on that probe order.
    // Styling off `e.from` alone is deterministic regardless of discovery
    // order, since dedupeEdges always keeps the same (from, to) pair: dashed
    // when the call's actual source is test code, dotted when either
    // endpoint is an unresolved file-kind reference (checked on `from` first
    // so a file-kind source always wins, matching edgeStyleAttrs' own
    // precedence).
    const attrs = e.to.kind === 'file' && e.from.kind !== 'file' ? edgeStyleAttrs(e.to) : edgeStyleAttrs(e.from);
    const style = attrs.length ? ` [${attrs.join(', ')}]` : '';
    lines.push(`  "${esc(idOf(e.from))}" -> "${esc(idOf(e.to))}"${style};`);
  }
  lines.push('}');
  return lines.join('\n');
}

// Sequential — same concurrency hazard collectTransitive/probeFileEdges note:
// parallel codegraph calls against one index race on its schema_versions
// table. `fatal: false` on each probe lets one bad root (an ambiguous or
// since-deleted name) skip past without aborting the rest of the diff.
async function runDiffMode(repoPath, { diffRef, limit, maxSymbols, maxRender, tooltips, embedFile }) {
  const changedFiles = gitDiffFiles(repoPath, diffRef);
  if (changedFiles.length === 0) {
    // With --embed, rendering the blank graph anyway would silently
    // overwrite a real, previously-committed diagram — see
    // diffEmbedRefusal. Without --embed there's nothing to protect, so
    // this stays the same warn-and-render-blank behavior as before.
    if (embedFile) {
      console.error(diffEmbedRefusal(diffRef, embedFile));
      process.exit(1);
    }
    console.error(diffNoChangesWarning(diffRef));
    // No point running enumerateAllSymbols' full-index query (potentially
    // slow/large, per its own comment) when there is nothing it could match —
    // an empty diff can only ever produce an empty root set.
    return buildDiffDot([], [], { maxRender, tooltips });
  }

  const allSymbols = await enumerateAllSymbols(repoPath);
  const matched = matchRootSymbols(allSymbols, changedFiles);
  if (matched.length === 0) console.error(diffNoSymbolsWarning(repoPath, changedFiles.length));

  const budgetWarning = diffSymbolBudgetWarning(matched.length, maxSymbols);
  if (budgetWarning) console.error(budgetWarning);
  const roots = matched.slice(0, maxSymbols);

  // Same known limitation --architecture's bare-name probing has: two
  // symbols sharing a name across files are ambiguous to codegraph's
  // bare-name callers/callees query. Scoped to ALL of allSymbols whose name
  // matches a root's — not just roots-vs-roots — so a root colliding with
  // an unrelated, unchanged symbol elsewhere in the repo is caught too;
  // duplicateNameWarning(roots) alone would miss that (a diff touching just
  // one `parse` finds no duplicate among a 1-symbol root set even if the
  // repo has three). Reusing the existing warning rather than
  // reimplementing --architecture's heavier node -f fix keeps this v1
  // honest about the gap instead of hiding it.
  const rootNames = new Set(roots.map(r => r.name));
  const dupeWarning = duplicateNameWarning(allSymbols.filter(s => rootNames.has(s.name)));
  if (dupeWarning) console.error(dupeWarning);

  const edges = [];
  for (const root of roots) {
    const callersResult = await runCodegraph(['callers', '--path', repoPath, '--limit', String(limit), '--json', '--', root.name], { fatal: false });
    for (const c of (callersResult?.callers || [])) edges.push({ from: c, to: root });
    const calleesResult = await runCodegraph(['callees', '--path', repoPath, '--limit', String(limit), '--json', '--', root.name], { fatal: false });
    for (const c of (calleesResult?.callees || [])) edges.push({ from: root, to: c });
  }

  const dedupedEdges = dedupeEdges(edges);
  const rootKeys = new Set(roots.map(r => `${r.name} ${r.filePath}`));
  const nonRootNodes = dedupeNodes(dedupedEdges.flatMap(e => [e.from, e.to]))
    .filter(n => !rootKeys.has(`${n.name} ${n.filePath}`));
  const note = renderTruncationNote('callers/callees', nonRootNodes.length, maxRender);
  if (note) console.error(note);

  const rootsWithEdges = new Set();
  for (const e of dedupedEdges) {
    const fk = `${e.from.name} ${e.from.filePath}`, tk = `${e.to.name} ${e.to.filePath}`;
    if (rootKeys.has(fk)) rootsWithEdges.add(fk);
    if (rootKeys.has(tk)) rootsWithEdges.add(tk);
  }
  const emptyRootsWarning = diffEmptyRootsWarning(roots.length - rootsWithEdges.size, roots.length);
  if (emptyRootsWarning) console.error(emptyRootsWarning);

  return buildDiffDot(roots, edges, { maxRender, tooltips });
}

function renderDotToFile(dot, format, outFile) {
  const dotFile = path.join(os.tmpdir(), `codeshot-${Date.now()}.dot`);
  fs.writeFileSync(dotFile, dot, 'utf8');
  execFileSync('dot', [`-T${format}`, dotFile, '-o', outFile]);
  fs.unlinkSync(dotFile);
}

// Renders the same way as renderDotToFile but returns the image bytes instead
// of writing them — used by --embed --check to regenerate a diagram in memory
// and byte-compare it against the committed one, without touching the tree.
// `dot` writes identical bytes to a file (-o) or stdout, and its svg output
// embeds no timestamp or input path, so the two paths are directly comparable
// under one graphviz version (the same-generator caveat every regenerate-and-
// diff artifact check carries).
function renderDotToBuffer(dot, format) {
  const dotFile = path.join(os.tmpdir(), `codeshot-check-${Date.now()}.dot`);
  fs.writeFileSync(dotFile, dot, 'utf8');
  try {
    return execFileSync('dot', [`-T${format}`, dotFile]);
  } finally {
    fs.unlinkSync(dotFile);
  }
}

// graphviz XML-encodes a few characters inside node/edge <title>s — notably the
// '->' of an edge id becomes '&#45;&gt;' — so a parsed title has to be decoded
// back to the raw id codeshot wrote into the DOT. Handles the named entities
// graphviz emits plus numeric ones; '&amp;' is undone last so an already-encoded
// '&amp;lt;' doesn't get double-decoded.
function decodeXmlEntities(s) {
  return String(s)
    .replace(/&#[xX]([0-9a-fA-F]+);/g, (_, h) => String.fromCharCode(parseInt(h, 16)))
    .replace(/&#(\d+);/g, (_, d) => String.fromCharCode(Number(d)))
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&amp;/g, '&');
}

// The version-independent heart of --check for svg output: reduce a graphviz SVG
// to just its call STRUCTURE — the set of node ids and the set of directed edges
// — discarding layout coordinates, colors, fonts, and the graphviz version stamp,
// none of which are semantic drift. graphviz renders every node/edge as a
// `<g class="node|edge">` whose `<title>` is verbatim the id codeshot put in the
// DOT (a node's id, or an edge's "from->to"); that title text is identical across
// graphviz versions even though the surrounding geometry is not — which is exactly
// why a byte-compare of the rendered svg false-failed in CI whenever the committed
// image and the checking machine used different graphviz builds. Returns a
// canonical signature (nodes sorted, then edges sorted) so two svgs with the same
// graph but a different node emission ORDER — codegraph's enumeration order isn't
// guaranteed stable run-to-run — still compare equal. Cosmetic drift (a test edge
// losing its dash, a recolor) is deliberately NOT captured: only a node or a call
// appearing or disappearing changes the structure and fails the check. The regex
// tolerates either attribute order (`id` before or after `class`), which also
// varies by graphviz version, and skips the graph's own top-level <title>.
function svgStructure(svg) {
  const nodes = new Set();
  const edges = new Set();
  const re = /<g\b[^>]*\bclass="(node|edge)"[^>]*>\s*<title>([\s\S]*?)<\/title>/g;
  let m;
  while ((m = re.exec(String(svg))) !== null) {
    const title = decodeXmlEntities(m[2].trim());
    (m[1] === 'node' ? nodes : edges).add(title);
  }
  return `nodes:\n${[...nodes].sort().join('\n')}\nedges:\n${[...edges].sort().join('\n')}`;
}

// --embed keeps a generated diagram inside a committed markdown doc, refreshed
// in place — the same idempotent HTML-comment-marker pattern doctoc and
// terraform-docs use. Each embed is keyed by an id (`arch`, or the symbol
// name) so several distinct diagrams can live in one doc without clobbering
// each other.
function embedMarkers(markerId) {
  return { start: `<!-- codeshot:${markerId}:start -->`, end: `<!-- codeshot:${markerId}:end -->` };
}

function regexEscape(s) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// Pure: returns `content` with the codeshot block for `markerId` set to
// `markdown`. If the markers already exist, their contents are replaced in
// place (idempotent — re-running is a no-op when nothing changed); if neither
// marker is present, a fresh block is appended after a blank line; a lone
// start-or-end marker is malformed and throws rather than risk mangling the doc.
function applyEmbed(content, markerId, markdown) {
  const { start, end } = embedMarkers(markerId);
  const block = `${start}\n${markdown}\n${end}`;
  const hasStart = content.includes(start);
  const hasEnd = content.includes(end);
  if (hasStart && hasEnd) {
    const re = new RegExp(`${regexEscape(start)}[\\s\\S]*?${regexEscape(end)}`);
    return content.replace(re, () => block); // function replacement: '$' in markdown stays literal
  }
  if (hasStart !== hasEnd) {
    throw new Error(`--embed: markers for '${markerId}' are malformed in the target doc — found a ${hasStart ? 'start marker with no matching end' : 'end marker with no matching start'}. Fix or remove the stray '<!-- codeshot:${markerId}:... -->' comment and retry.`);
  }
  const trimmed = content.replace(/\s+$/, '');
  return (trimmed ? `${trimmed}\n\n` : '') + block + '\n';
}

// Markdown links use forward slashes on every platform, so the OS-specific
// separator from path.relative is normalized before it goes into the doc.
function embedRelLink(embedFile, imagePath) {
  const rel = path.relative(path.dirname(path.resolve(embedFile)), path.resolve(imagePath));
  return (rel || path.basename(imagePath)).split(path.sep).join('/');
}

// The single output tail for both modes: plain render, --embed (render + update
// the doc in place), or --embed --check (regenerate in memory and verify the
// committed image AND doc block are current, mutating nothing — the drift guard
// a CI job or pre-commit hook calls; exit 1 == stale, exit 0 == up to date).
function finishOutput(dot, { format, outFile, embedFile, check, markerId, alt }) {
  if (!embedFile) {
    renderDotToFile(dot, format, outFile);
    console.log(outFile);
    return;
  }

  const markdown = `![${alt}](${embedRelLink(embedFile, outFile)})`;

  let docContent;
  try {
    docContent = fs.readFileSync(embedFile, 'utf8');
  } catch {
    console.error(`codeshot: --embed target '${embedFile}' does not exist — --embed refreshes a diagram inside an existing doc, it does not create one.`);
    process.exit(1);
  }
  let expected;
  try {
    expected = applyEmbed(docContent, markerId, markdown); // also validates markers
  } catch (err) {
    console.error(`codeshot: ${err.message}`);
    process.exit(1);
  }

  if (check) {
    const fresh = renderDotToBuffer(dot, format);
    let committed = null;
    try { committed = fs.readFileSync(outFile); } catch { /* missing → stale */ }
    // svg: compare the graph structure (node/edge set), which is graphviz-version
    // independent, so a committed image rendered by one graphviz build and a fresh
    // render on another (e.g. a laptop vs CI) don't false-drift on layout/version
    // bytes — the reason a raw byte-compare made --check unusable in CI. Other
    // formats have no recoverable structure and keep the byte-compare (and its
    // documented same-graphviz-version caveat).
    let imageStale;
    if (!committed) {
      imageStale = true;
    } else if (STRUCTURAL_CHECK_FORMATS.has(format.toLowerCase())) {
      imageStale = svgStructure(committed.toString('utf8')) !== svgStructure(fresh.toString('utf8'));
    } else {
      imageStale = !committed.equals(fresh);
    }
    const docStale = docContent !== expected;
    if (imageStale || docStale) {
      if (imageStale) console.error(`codeshot: --check: diagram '${outFile}' is out of date (${committed ? 'differs from a fresh render' : 'missing'}) — rerun 'codeshot ... --embed ${embedFile}' and commit the result.`);
      if (docStale) console.error(`codeshot: --check: the codeshot:${markerId} block in '${embedFile}' is out of date or missing — rerun 'codeshot ... --embed ${embedFile}' and commit the result.`);
      process.exit(1);
    }
    console.log(`codeshot: up to date — '${outFile}' and the codeshot:${markerId} block in '${embedFile}' match a fresh render.`);
    return;
  }

  renderDotToFile(dot, format, outFile);
  if (expected !== docContent) {
    fs.writeFileSync(embedFile, expected, 'utf8');
    console.error(`codeshot: updated the codeshot:${markerId} block in ${embedFile}`);
  }
  console.log(outFile);
}

const USAGE = 'Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--format <fmt>] [--depth <n>] [--max-depth-nodes <n>] [--embed <file.md> [--check]]\n   or: callgraph.js --architecture [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--max-symbols <n>] [--group-depth <n>] [--format <fmt>] [--embed <file.md> [--check]]\n   or: callgraph.js --diff [--diff-ref <range>] [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--max-symbols <n>] [--format <fmt>] [--embed <file.md> [--check]]';

async function main() {
  let values, positionals;
  try {
    ({ values, positionals } = parseArgs({
      args: process.argv.slice(2),
      options: {
        version: { type: 'boolean', short: 'v' },
        path:  { type: 'string', default: '.' },
        out:   { type: 'string' },
        limit: { type: 'string', default: String(DEFAULT_LIMIT) },
        'max-render': { type: 'string' },
        format: { type: 'string', default: 'png' },
        depth: { type: 'string', default: '1' },
        'max-depth-nodes': { type: 'string' },
        architecture: { type: 'boolean', default: false },
        'max-symbols': { type: 'string', default: String(DEFAULT_MAX_SYMBOLS) },
        'group-depth': { type: 'string' },
        diff: { type: 'boolean', default: false },
        'diff-ref': { type: 'string' },
        embed: { type: 'string' },
        check: { type: 'boolean', default: false },
      },
      allowPositionals: true,
    }));
  } catch (err) {
    if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--limit/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--limit');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --limit must be a positive integer, got '${badValue}'`);
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--max-render/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--max-render');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --max-render must be a positive integer, got '${badValue}'`);
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--depth/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--depth');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --depth must be a positive integer, got '${badValue}'`);
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--max-depth-nodes/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--max-depth-nodes');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --max-depth-nodes must be a positive integer, got '${badValue}'`);
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--max-symbols/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--max-symbols');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --max-symbols must be a positive integer, got '${badValue}'`);
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--group-depth/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--group-depth');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --group-depth must be a positive integer, got '${badValue}'`);
    } else {
      console.error(`codeshot: ${err.message}`);
    }
    console.error(USAGE);
    process.exit(1);
  }

  // --version resolves before any required-argument check so it works standalone.
  // package.json is the single source of truth for the version (npm-native); the
  // release CI asserts the git tag matches it, so they cannot drift.
  if (values.version) {
    console.log(`codeshot ${require('../package.json').version}`);
    process.exit(0);
  }

  const symbol = positionals[0];
  const diffMode = values.diff || values['diff-ref'] !== undefined;
  if (values.architecture && diffMode) {
    console.error('codeshot: --architecture cannot be combined with --diff');
    console.error(USAGE);
    process.exit(1);
  }
  if ((values.architecture || diffMode) && symbol) {
    console.error(`codeshot: --${values.architecture ? 'architecture' : 'diff'} cannot be combined with a <symbol> argument`);
    console.error(USAGE);
    process.exit(1);
  }
  if (!values.architecture && !diffMode && !symbol) {
    console.error('codeshot: missing required <symbol> argument');
    console.error(USAGE);
    process.exit(1);
  }
  if (values['diff-ref'] === '') {
    console.error('codeshot: --diff-ref must not be empty');
    process.exit(1);
  }

  if (values.path === '') {
    console.error('codeshot: --path must not be empty');
    process.exit(1);
  }
  const repoPath = values.path;
  if (values.out === '') {
    console.error('codeshot: --out must not be empty');
    process.exit(1);
  }
  let   outFile  = values.out || null;
  const limit    = Number(values.limit);
  if (!Number.isInteger(limit) || limit <= 0) {
    console.error(`codeshot: --limit must be a positive integer, got '${values.limit}'`);
    process.exit(1);
  }
  let maxRender;
  if (values['max-render'] !== undefined) {
    maxRender = Number(values['max-render']);
    if (!Number.isInteger(maxRender) || maxRender <= 0) {
      console.error(`codeshot: --max-render must be a positive integer, got '${values['max-render']}'`);
      process.exit(1);
    }
  }
  if (values.format === '') {
    console.error('codeshot: --format must not be empty');
    process.exit(1);
  }
  const format = values.format;
  const depth  = Number(values.depth);
  if (!Number.isInteger(depth) || depth <= 0) {
    console.error(`codeshot: --depth must be a positive integer, got '${values.depth}'`);
    process.exit(1);
  }
  if ((values.architecture || diffMode) && values.depth !== '1') {
    const mode = values.architecture ? '--architecture' : '--diff';
    const reason = values.architecture ? 'there is no multi-hop file traversal' : 'diff mode only draws direct callers/callees around each changed symbol';
    console.error(`codeshot: --depth has no effect with ${mode} (${reason})`);
    process.exit(1);
  }
  let maxDepthNodes = DEFAULT_NODE_BUDGET;
  if (values['max-depth-nodes'] !== undefined) {
    maxDepthNodes = Number(values['max-depth-nodes']);
    if (!Number.isInteger(maxDepthNodes) || maxDepthNodes <= 0) {
      console.error(`codeshot: --max-depth-nodes must be a positive integer, got '${values['max-depth-nodes']}'`);
      process.exit(1);
    }
    if ((values.architecture || diffMode) && maxDepthNodes !== DEFAULT_NODE_BUDGET) {
      const mode = values.architecture ? '--architecture' : '--diff';
      console.error(`codeshot: --max-depth-nodes has no effect with ${mode} (there is no multi-hop traversal)`);
      process.exit(1);
    }
  }
  const maxSymbols = Number(values['max-symbols']);
  if (!Number.isInteger(maxSymbols) || maxSymbols <= 0) {
    console.error(`codeshot: --max-symbols must be a positive integer, got '${values['max-symbols']}'`);
    process.exit(1);
  }
  // Rejected outside --architecture rather than silently ignored (the stance
  // --depth/--max-depth-nodes take in the opposite direction, not --max-symbols'
  // quiet no-op): there is no file-level graph to roll up in symbol mode, so a
  // --group-depth there is always a mistake, and a silently-dropped flag reads
  // as "grouping applied" in exactly the diagram you'd then trust.
  let groupDepth;
  if (values['group-depth'] !== undefined) {
    groupDepth = Number(values['group-depth']);
    if (!Number.isInteger(groupDepth) || groupDepth <= 0) {
      console.error(`codeshot: --group-depth must be a positive integer, got '${values['group-depth']}'`);
      process.exit(1);
    }
    if (!values.architecture) {
      console.error('codeshot: --group-depth only applies with --architecture (a symbol trail has no file-level graph to roll up)');
      process.exit(1);
    }
  }
  if (values.embed === '') {
    console.error('codeshot: --embed must not be empty');
    process.exit(1);
  }
  const embedFile = values.embed || null;
  if (values.check && !embedFile) {
    console.error('codeshot: --check only applies with --embed (it verifies an embedded diagram is current)');
    process.exit(1);
  }
  // Validate the embed target up front — before the codegraph/dot PATH checks
  // and any expensive querying — so a bad --embed path fails fast with a clear
  // message rather than after a multi-minute --architecture scan (and rather
  // than being masked by a missing-codegraph error on a machine without it).
  if (embedFile && !fs.existsSync(embedFile)) {
    console.error(`codeshot: --embed target '${embedFile}' does not exist — --embed refreshes a diagram inside an existing doc, it does not create one.`);
    process.exit(1);
  }

  const safeSymbol = (values.architecture || diffMode) ? null : sanitizeForFilename(symbol);
  // Suffixed by --diff-ref, same reason --group-depth gets its own suffix
  // below: two --diff diagrams scoped to different ranges (e.g. a release
  // diff and a PR diff) must not collide on one stable --embed path/marker
  // id and silently overwrite each other. Unset --diff-ref keeps the plain
  // 'diff' id/name, so a bare `--diff` embed is unaffected.
  const diffSuffix = values['diff-ref'] ? `-${sanitizeForFilename(values['diff-ref'])}` : '';
  const diffMarkerId = `diff${diffSuffix}`; // built once, shared by the default filename below and finishOutput's markerId
  if (!outFile) {
    const archBase = groupDepth ? `arch-d${groupDepth}-${architectureOutputBaseName(repoPath)}` : `arch-${architectureOutputBaseName(repoPath)}`;
    const base = values.architecture ? archBase : diffMode ? `${diffMarkerId}-${architectureOutputBaseName(repoPath)}` : `callgraph-${safeSymbol}`;
    // With --embed the image must live at a STABLE path next to the doc — so the
    // relative link resolves, the file can be committed, and a re-run overwrites
    // the same file rather than littering tmp with timestamped copies.
    outFile = embedFile
      ? path.join(path.dirname(path.resolve(embedFile)), `codeshot-${base}.${format}`)
      : path.join(os.tmpdir(), `${base}-${Date.now()}.${format}`);
  } else {
    const mismatchWarning = formatMismatchWarning(outFile, format);
    if (mismatchWarning) console.error(mismatchWarning);
  }

  requireOnPath('codegraph', 'Install: https://github.com/colbymchenry/codegraph');
  requireOnPath('dot', 'Install graphviz (e.g. `brew install graphviz` or `apt install graphviz`).');
  if (diffMode) requireOnPath('git', 'Install git (e.g. `apt install git`) — --diff shells out to `git diff` to find changed files.');

  // Warn before doing any work if the index is mid-rebuild — a silently-partial
  // graph is worse than a slow one, and node count alone can't reveal it.
  const healthWarning = indexHealthWarning(repoPath);
  if (healthWarning) console.error(healthWarning);

  // Node tooltips only render in svg-family output (graphviz emits them as
  // <a xlink:title>); computed here (rather than just before symbol mode's
  // buildDot call) so --diff's buildDiffDot can use it too.
  const tooltips = SVG_TOOLTIP_FORMATS.has(format.toLowerCase());

  if (diffMode) {
    const dot = await runDiffMode(repoPath, { diffRef: values['diff-ref'] || null, limit, maxSymbols, maxRender, tooltips, embedFile });
    const alt = `Diff-scoped call graph${values['diff-ref'] ? ` (${values['diff-ref']})` : ''} — generated by codeshot`;
    finishOutput(dot, { format, outFile, embedFile, check: values.check, markerId: diffMarkerId, alt });
    return;
  }

  if (values.architecture) {
    const dot = await runArchitectureMode(repoPath, { limit, maxSymbols, maxRender, groupDepth });
    // Fixed, path-independent alt: deriving it from the checkout's directory
    // basename made the embedded markdown vary by where the repo was cloned
    // (a bare-worktree dir, "master", a branch name...), which both read wrong
    // and broke --check portability — a fresh clone under a different dir name
    // would report the committed diagram as drifted. The repo name is redundant
    // anyway; the diagram lives in that repo's own doc.
    // The grouped view gets its own marker id (and default image name), so
    // embedding it doesn't silently overwrite an ungrouped `codeshot:arch`
    // block already committed in the same doc — the per-file and per-module
    // pictures answer different questions and a repo may reasonably want both.
    // Unset --group-depth keeps the plain 'arch' id, so existing docs are
    // untouched.
    const alt = groupDepth
      ? `Architecture (grouped by directory, depth ${groupDepth}) — generated by codeshot`
      : 'Architecture — generated by codeshot';
    finishOutput(dot, { format, outFile, embedFile, check: values.check, markerId: groupDepth ? `arch-d${groupDepth}` : 'arch', alt });
    return;
  }

  // Sequential, not Promise.all: concurrent codegraph invocations against the
  // same SQLite index intermittently race on codegraph's own schema_versions
  // table ("UNIQUE constraint failed"), confirmed by running these calls in
  // parallel — codegraph is not safe to invoke concurrently against one index.
  // '--' before the symbol: codegraph's own arg parser otherwise misreads a
  // symbol starting with '-' (e.g. a mangled/generated name) as a flag.
  const resolvedSymbol = await resolveSymbol(symbol, repoPath);
  const { callers } = await runCodegraph(['callers', '--path', repoPath, '--limit', String(limit), '--json', '--', resolvedSymbol]);
  const { callees } = await runCodegraph(['callees', '--path', repoPath, '--limit', String(limit), '--json', '--', resolvedSymbol]);

  for (const [kind, results] of [['callers', callers || []], ['callees', callees || []]]) {
    const warning = truncationWarning(kind, results, limit);
    if (warning) console.error(warning);
  }

  const emptyWarning = emptyGraphWarning(resolvedSymbol, callers, callees);
  if (emptyWarning) console.error(emptyWarning);

  let transitiveEdges = [];
  if (depth > 1) {
    const discovered = new Set(dedupeNodes([...(callers || []), ...(callees || [])]).map(n => `${n.name} ${n.filePath}`));
    const callerResult = await collectTransitive('callers', repoPath, limit, depth, dedupeNodes(callers || []), discovered, maxDepthNodes);
    const calleeResult = await collectTransitive('callees', repoPath, limit, depth, dedupeNodes(callees || []), discovered, maxDepthNodes);
    transitiveEdges = [...callerResult.edges, ...calleeResult.edges];
    const budgetWarning = depthBudgetWarning(callerResult.truncated || calleeResult.truncated, maxDepthNodes);
    if (budgetWarning) console.error(budgetWarning);
  }

  // Same shared-budget allocation buildDot uses internally, computed here too
  // so these stderr notes report what actually got drawn, not each dimension's
  // raw distinct count against the full --max-render value.
  const distinctCallers = dedupeNodes(callers || []).length;
  const distinctCallees = dedupeNodes(callees || []).length;
  const distinctTransitive = dedupeEdges(transitiveEdges).length;
  const [callerBudget, calleeBudget, transitiveBudget] = allocateRenderBudget(
    maxRender, [distinctCallers, distinctCallees, distinctTransitive]
  );
  for (const [kind, distinctCount, budget] of [
    ['callers', distinctCallers, callerBudget],
    ['callees', distinctCallees, calleeBudget],
    ...(transitiveEdges.length ? [['transitive edges', distinctTransitive, transitiveBudget]] : []),
  ]) {
    const note = renderTruncationNote(kind, distinctCount, budget);
    if (note) console.error(note);
  }

  const dot = buildDot(resolvedSymbol, callers || [], callees || [], { maxRender, transitiveEdges, tooltips });
  const alt = `${resolvedSymbol} call graph — generated by codeshot`;
  finishOutput(dot, { format, outFile, embedFile, check: values.check, markerId: safeSymbol, alt });
}

if (require.main === module) {
  main().catch(err => {
    console.error(`codeshot: ${err.message}`);
    process.exit(1);
  });
}

module.exports = {
  buildDot, nodeIdentities, isTestRef, truncationWarning, dedupeNodes, renderTruncationNote, dedupeEdges, depthColor,
  depthBudgetWarning, allocateRenderBudget, formatMismatchWarning, matchSymbolNotFound,
  unwrapQueryNodes, symbolBudgetWarning, duplicateNameWarning, duplicateNames, parseNodeCalls, aggregateFileEdges,
  topFilesByWeight, buildArchitectureDot, architectureOutputBaseName,
  groupPath, rollupFileEdges, groupCollapseWarning, sortSymbolsForEnumeration,
  applyEmbed, embedMarkers, embedRelLink, parseUnresolvedRefs,
  svgStructure, decodeXmlEntities,
  emptyGraphWarning, emptyArchitectureWarning,
  matchNotInitialized, argRepoPath, parseCodegraphOutput,
  matchRootSymbols, diffNoChangesWarning, diffNoSymbolsWarning, diffSymbolBudgetWarning, buildDiffDot,
  diffEmbedRefusal, diffEmptyRootsWarning,
};
