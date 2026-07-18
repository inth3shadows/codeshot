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

// Multi-hop traversal (--depth > 1) makes one sequential codegraph call per
// newly discovered node, per hop — on a well-connected symbol that fans out
// fast. This caps total discovered nodes across both directions combined so
// one request can't turn into hundreds of sequential codegraph invocations.
// Not exposed as a flag (yet): a fixed safety cap, not a tuning knob.
const NODE_BUDGET = 200;

// --architecture probes every enumerated symbol sequentially (one codegraph
// call each) to build the file-level graph — a multi-minute operation on a
// mid-size repo, unlike NODE_BUDGET's quiet safety net on an already-fast
// --depth traversal. Users legitimately need to trade coverage for speed
// themselves, so this — deliberately, unlike NODE_BUDGET — IS exposed as a
// flag (--max-symbols).
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

// `fatal` (default true) matches every existing call site's behavior. Pass
// `fatal: false` when calling this in a loop that probes many symbols and
// must survive an individual bad one (e.g. --architecture's enumeration) —
// process.exit() would otherwise kill the whole scan, and a bare try/catch
// around runCodegraph does NOT catch process.exit().
function parseCodegraphOutput(out, args, { fatal = true } = {}) {
  const notFound = matchSymbolNotFound(out);
  if (notFound !== null) {
    if (!fatal) return null;
    console.error(`codeshot: symbol '${notFound}' not found in codegraph's index — check the spelling/casing, or confirm --path points at the repo that contains it.`);
    process.exit(1);
  }
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
async function runCodegraph(args, { fatal = true } = {}) {
  const { stdout } = await execFileAsync('codegraph', args, { encoding: 'utf8', maxBuffer: MAX_CODEGRAPH_BUFFER });
  return parseCodegraphOutput(stdout, args, { fatal });
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
  return `codeshot: --depth traversal stopped early (internal safety cap of ${budget} discovered nodes) — the graph beyond this point is incomplete; rerun with a smaller --depth or --limit, or a more specific symbol, to stay under the cap.`;
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
// callers/callees' flat `{ name, kind, filePath }` shape. A "kind":"file"
// entry is the file object itself, not a callable symbol — codegraph has no
// `callees <file>` concept, so it's dropped rather than probed.
function filterCallableSymbols(queryResults) {
  return (queryResults || [])
    .map(r => r.node)
    .filter(n => n && n.kind !== 'file');
}

function symbolBudgetWarning(truncated, budget) {
  if (!truncated) return null;
  return `codeshot: --architecture stopped enumerating after ${budget} symbols (--max-symbols) — the graph is incomplete; rerun with a larger --max-symbols to cover the rest of the repo.`;
}

// codegraph's callers/callees take a bare name with no --file disambiguation
// (unlike `codegraph node -f`), so two same-named symbols in different files
// are genuinely ambiguous to a `codegraph callees <name>` probe — a real risk
// at --architecture's scale (probing hundreds of names), not a corner case.
function duplicateNameWarning(symbols) {
  const counts = new Map();
  for (const s of symbols || []) counts.set(s.name, (counts.get(s.name) || 0) + 1);
  const dupes = [...counts.entries()].filter(([, n]) => n > 1).map(([name]) => name);
  if (!dupes.length) return null;
  const examples = dupes.slice(0, 3).join(', ');
  return `codeshot: ${dupes.length} symbol name(s) appear in more than one file (e.g. ${examples}) — codegraph's callees can't disambiguate by file, so edges for these may be attributed to the wrong file.`;
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
async function enumerateSymbols(repoPath, maxSymbols) {
  const results = await runCodegraph(['query', '--path', repoPath, '--json', '--limit', String(ENUMERATION_QUERY_LIMIT), '--', '']);
  const symbols = filterCallableSymbols(results);
  const truncated = symbols.length > maxSymbols;
  return { symbols: symbols.slice(0, maxSymbols), truncated };
}

// Sequential — same concurrency hazard as collectTransitive: parallel
// codegraph calls against one index race on its schema_versions table.
// fatal:false + the null check below is what lets one ambiguous/not-found
// probed name (real and expected at this scale — see duplicateNameWarning)
// skip past without aborting the whole multi-minute scan.
async function probeFileEdges(symbols, repoPath, limit) {
  const edges = [];
  for (let i = 0; i < symbols.length; i++) {
    const s = symbols[i];
    const result = await runCodegraph(
      ['callees', '--path', repoPath, '--limit', String(limit), '--json', '--', s.name],
      { fatal: false }
    );
    if (result === null) continue;
    for (const c of result.callees || []) {
      edges.push({ fromFile: s.filePath, toFile: c.filePath });
    }
    if ((i + 1) % 25 === 0) {
      console.error(`codeshot: scanned ${i + 1}/${symbols.length} symbols...`);
    }
  }
  return edges;
}

async function runArchitectureMode(repoPath, { limit, maxSymbols, maxRender }) {
  const { symbols, truncated } = await enumerateSymbols(repoPath, maxSymbols);
  const symbolWarning = symbolBudgetWarning(truncated, maxSymbols);
  if (symbolWarning) console.error(symbolWarning);
  const dupeWarning = duplicateNameWarning(symbols);
  if (dupeWarning) console.error(dupeWarning);

  const symbolEdges = await probeFileEdges(symbols, repoPath, limit);
  const fileEdges = aggregateFileEdges(symbolEdges);

  const totalFiles = new Set(fileEdges.flatMap(e => [e.from, e.to])).size;
  const note = renderTruncationNote('files', totalFiles, maxRender);
  if (note) console.error(note);

  return buildArchitectureDot(fileEdges, { maxRender });
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
    const imageStale = !committed || !committed.equals(fresh);
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

const USAGE = 'Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--format <fmt>] [--depth <n>] [--embed <file.md> [--check]]\n   or: callgraph.js --architecture [--path <repoPath>] [--out <file.png>] [--limit <n>] [--max-render <n>] [--max-symbols <n>] [--format <fmt>] [--embed <file.md> [--check]]';

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
        architecture: { type: 'boolean', default: false },
        'max-symbols': { type: 'string', default: String(DEFAULT_MAX_SYMBOLS) },
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
    } else if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--max-symbols/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--max-symbols');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --max-symbols must be a positive integer, got '${badValue}'`);
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
  if (values.architecture && symbol) {
    console.error('codeshot: --architecture cannot be combined with a <symbol> argument');
    console.error(USAGE);
    process.exit(1);
  }
  if (!values.architecture && !symbol) {
    console.error('codeshot: missing required <symbol> argument');
    console.error(USAGE);
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
  if (values.architecture && values.depth !== '1') {
    console.error('codeshot: --depth has no effect with --architecture (there is no multi-hop file traversal)');
    process.exit(1);
  }
  const maxSymbols = Number(values['max-symbols']);
  if (!Number.isInteger(maxSymbols) || maxSymbols <= 0) {
    console.error(`codeshot: --max-symbols must be a positive integer, got '${values['max-symbols']}'`);
    process.exit(1);
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

  const safeSymbol = values.architecture ? null : sanitizeForFilename(symbol);
  if (!outFile) {
    const base = values.architecture ? `arch-${architectureOutputBaseName(repoPath)}` : `callgraph-${safeSymbol}`;
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

  if (values.architecture) {
    const dot = await runArchitectureMode(repoPath, { limit, maxSymbols, maxRender });
    // Fixed, path-independent alt: deriving it from the checkout's directory
    // basename made the embedded markdown vary by where the repo was cloned
    // (a bare-worktree dir, "master", a branch name...), which both read wrong
    // and broke --check portability — a fresh clone under a different dir name
    // would report the committed diagram as drifted. The repo name is redundant
    // anyway; the diagram lives in that repo's own doc.
    const alt = 'Architecture — generated by codeshot';
    finishOutput(dot, { format, outFile, embedFile, check: values.check, markerId: 'arch', alt });
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

  let transitiveEdges = [];
  if (depth > 1) {
    const discovered = new Set(dedupeNodes([...(callers || []), ...(callees || [])]).map(n => `${n.name} ${n.filePath}`));
    const callerResult = await collectTransitive('callers', repoPath, limit, depth, dedupeNodes(callers || []), discovered, NODE_BUDGET);
    const calleeResult = await collectTransitive('callees', repoPath, limit, depth, dedupeNodes(callees || []), discovered, NODE_BUDGET);
    transitiveEdges = [...callerResult.edges, ...calleeResult.edges];
    const budgetWarning = depthBudgetWarning(callerResult.truncated || calleeResult.truncated, NODE_BUDGET);
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

  // Node tooltips only render in svg-family output (graphviz emits them as
  // <a xlink:title>); they're inert in png/pdf, so gate them to svg/svgz to
  // avoid bloating a raster diagram's intermediate DOT with dead attributes.
  const tooltips = SVG_TOOLTIP_FORMATS.has(format.toLowerCase());
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
  filterCallableSymbols, symbolBudgetWarning, duplicateNameWarning, aggregateFileEdges,
  topFilesByWeight, buildArchitectureDot, architectureOutputBaseName,
  applyEmbed, embedMarkers, embedRelLink,
};
