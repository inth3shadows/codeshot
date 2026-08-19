#!/usr/bin/env node
'use strict';

const assert = require('assert');
const {
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
  diffEmbedRefusal, diffEmptyRootsWarning, diffEmbedRefusalNoSymbols, diffDuplicateNameWarning,
  diffHandleEmptyRoots, diffTruncationWarning, nodeKey, diffNothingToCheck, diffNothingToCheckNoSymbols,
} = require('../render/callgraph.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed++;
    console.log(`ok - ${name}`);
  } catch (err) {
    failed++;
    console.log(`not ok - ${name}`);
    console.error(err);
  }
}

test('isTestRef matches on name', () => {
  assert.strictEqual(isTestRef({ name: 'shouldDoThingTest', filePath: 'src/thing.js' }), true);
});

test('isTestRef matches on filePath', () => {
  assert.strictEqual(isTestRef({ name: 'doThing', filePath: 'test/thing.spec.js' }), true);
});

test('isTestRef false for production code', () => {
  assert.strictEqual(isTestRef({ name: 'doThing', filePath: 'src/thing.js' }), false);
});

test('isTestRef does not misfire on "test"/"contest" as a substring', () => {
  assert.strictEqual(isTestRef({ name: 'AttestationService', filePath: 'src/attestation.js' }), false);
  assert.strictEqual(isTestRef({ name: 'ContestWinner', filePath: 'src/contest.js' }), false);
  assert.strictEqual(isTestRef({ name: 'Attest', filePath: 'src/attest.js' }), false);
});

test('isTestRef recognizes a spec/ directory layout', () => {
  assert.strictEqual(isTestRef({ name: 'Thing', filePath: 'spec/thing.js' }), true);
});

test('buildDot emits digraph header and highlighted symbol node', () => {
  const dot = buildDot('RollAutoSnapshot', [], []);
  assert.match(dot, /^digraph callgraph \{/);
  assert.match(dot, /"RollAutoSnapshot" \[fillcolor="#e2e8f0"/);
});

test('buildDot draws caller -> symbol and symbol -> callee edges', () => {
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'src/caller.js' }], [{ name: 'Callee', filePath: 'src/callee.js' }]);
  assert.match(dot, /"Caller" -> "Target";/);
  assert.match(dot, /"Target" -> "Callee";/);
});

test('buildDot dashes edges from test callers', () => {
  const dot = buildDot('Target', [{ name: 'CallerTest', filePath: 'src/caller.test.js' }], []);
  assert.match(dot, /"CallerTest" -> "Target" \[style=dashed, label="test"\];/);
});

test('buildDot escapes double quotes in names', () => {
  const dot = buildDot('Weird"Name', [], []);
  assert.match(dot, /"Weird\\"Name"/);
});

test('buildDot dedupes repeated caller/callee entries', () => {
  const dupeCaller = { name: 'Caller', filePath: 'src/caller.js' };
  const dupeCallee = { name: 'Callee', filePath: 'src/callee.js' };
  const dot = buildDot('Target', [dupeCaller, { ...dupeCaller }], [dupeCallee, { ...dupeCallee }]);
  assert.strictEqual((dot.match(/"Caller" -> "Target"/g) || []).length, 1);
  assert.strictEqual((dot.match(/"Target" -> "Callee"/g) || []).length, 1);
});

test('buildDot keeps distinct callers/callees with the same name but different filePath (unique graphviz ids)', () => {
  // Two callers named "Caller" in different files are distinct symbols. Graphviz
  // keys a node by the string in the edge, so emitting both as `"Caller" -> "Target"`
  // would collapse them into ONE box (see the render-level test below). Each must
  // therefore get a file-qualified id AND a disambiguating label.
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'a.js' }, { name: 'Caller', filePath: 'b.js' }], []);
  assert.match(dot, /"Caller@@a\.js" -> "Target";/);
  assert.match(dot, /"Caller@@b\.js" -> "Target";/);
  assert.doesNotMatch(dot, /"Caller" -> "Target"/, 'the bare shared id must NOT appear — that is the collapse this guards against');
  assert.match(dot, /"Caller@@a\.js" \[label="Caller\\n\(a\.js\)"\];/);
  assert.match(dot, /"Caller@@b\.js" \[label="Caller\\n\(b\.js\)"\];/);
});

test('buildDot renders same-named callers as TWO distinct boxes in the real image, not one', () => {
  // The render-level guard: buildDot's DOT text can look right while graphviz
  // still merges nodes. Prove the fix by counting nodes graphviz actually draws.
  const { execFileSync } = require('child_process');
  const dot = buildDot('Target', [{ name: 'handle', filePath: 'a.js' }, { name: 'handle', filePath: 'b.js' }], []);
  let svg;
  try {
    svg = execFileSync('dot', ['-Tsvg'], { input: dot, encoding: 'utf8' });
  } catch (err) {
    if (err.code === 'ENOENT') { console.log('  # skipped: `dot` (graphviz) not on PATH'); return; }
    throw new Error(`dot rejected buildDot's output: ${err.stderr || err.message}`);
  }
  // 3 nodes: the root "Target" plus the two distinct "handle" callers.
  assert.strictEqual((svg.match(/class="node"/g) || []).length, 3, 'expected 3 rendered node boxes (Target + two distinct handles), not a collapsed 2');
});

test('nodeIdentities leaves a name that lives in a single file as name-as-id with no label', () => {
  const { idOf, labelOf } = nodeIdentities([{ name: 'solo', filePath: 'a.js' }]);
  assert.strictEqual(idOf({ name: 'solo', filePath: 'a.js' }), 'solo');
  assert.strictEqual(labelOf({ name: 'solo', filePath: 'a.js' }), null);
});

test('nodeIdentities disambiguates a caller that shares the queried symbol\'s name away from the root', () => {
  // Root "handle" (filePath null) + a caller "handle" from a real file must not
  // share an id, or the caller draws as a self-loop on the root box.
  const { idOf } = nodeIdentities([{ name: 'handle', filePath: null }, { name: 'handle', filePath: 'b.js' }]);
  assert.strictEqual(idOf({ name: 'handle', filePath: 'b.js' }), 'handle@@b.js');
});

test('buildDot omits tooltips by default (png-safe, byte-identical to before)', () => {
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'src/caller.js' }], []);
  assert.doesNotMatch(dot, /tooltip=/, 'no tooltip attr should appear unless explicitly requested');
});

test('buildDot with tooltips:true declares each node with its filePath as a tooltip', () => {
  const dot = buildDot('Target',
    [{ name: 'Caller', filePath: 'src/caller.js' }],
    [{ name: 'Callee', filePath: 'src/callee.js' }],
    { tooltips: true });
  assert.match(dot, /"Caller" \[tooltip="src\/caller\.js"\];/);
  assert.match(dot, /"Callee" \[tooltip="src\/callee\.js"\];/);
});

test('buildDot tooltips combine with a disambiguating label on a colliding node', () => {
  const dot = buildDot('Target',
    [{ name: 'handle', filePath: 'a.js' }, { name: 'handle', filePath: 'b.js' }], [],
    { tooltips: true });
  assert.match(dot, /"handle@@a\.js" \[label="handle\\n\(a\.js\)", tooltip="a\.js"\];/);
});

test('buildDot tooltips reach the rendered svg as hover-able <a xlink:title>', () => {
  const { execFileSync } = require('child_process');
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'src/caller.js' }], [], { tooltips: true });
  let svg;
  try {
    svg = execFileSync('dot', ['-Tsvg'], { input: dot, encoding: 'utf8' });
  } catch (err) {
    if (err.code === 'ENOENT') { console.log('  # skipped: `dot` (graphviz) not on PATH'); return; }
    throw new Error(`dot rejected buildDot's output: ${err.stderr || err.message}`);
  }
  assert.match(svg, /xlink:title="src\/caller\.js"/, 'expected the caller node to carry its file path as an svg hover title');
});

test('buildDot renders every caller/callee when maxRender is omitted', () => {
  const many = Array.from({ length: 5 }, (_, i) => ({ name: `Fn${i}`, filePath: `src/fn${i}.js` }));
  const dot = buildDot('Target', many, many);
  assert.strictEqual((dot.match(/-> "Target"/g) || []).length, 5);
  assert.strictEqual((dot.match(/"Target" ->/g) || []).length, 5);
});

test('buildDot treats maxRender as ONE shared budget across callers and callees, not an independent cap per direction', () => {
  const many = Array.from({ length: 5 }, (_, i) => ({ name: `Fn${i}`, filePath: `src/fn${i}.js` }));
  const dot = buildDot('Target', many, many, { maxRender: 2 });
  // callers are spent first (priority order: direct trail before anything else),
  // so with a shared budget of 2 the callees get none — NOT 2 callers + 2 callees.
  assert.strictEqual((dot.match(/-> "Target"/g) || []).length, 2);
  assert.strictEqual((dot.match(/"Target" ->/g) || []).length, 0);
  assert.match(dot, /"Fn0" -> "Target"/);
  assert.match(dot, /"Fn1" -> "Target"/);
  assert.doesNotMatch(dot, /"Fn2" -> "Target"/);
});

test('buildDot\'s shared maxRender budget spends what callers leave over on callees', () => {
  const callers = Array.from({ length: 3 }, (_, i) => ({ name: `Caller${i}`, filePath: `src/c${i}.js` }));
  const callees = Array.from({ length: 5 }, (_, i) => ({ name: `Callee${i}`, filePath: `src/e${i}.js` }));
  const dot = buildDot('Target', callers, callees, { maxRender: 5 });
  assert.strictEqual((dot.match(/-> "Target"/g) || []).length, 3, 'all 3 callers fit');
  assert.strictEqual((dot.match(/"Target" ->/g) || []).length, 2, 'only 2 of 5 callees fit the remaining budget');
});

test('buildDot maxRender applies after dedup, not before', () => {
  const dupe = { name: 'Caller', filePath: 'src/caller.js' };
  const dot = buildDot('Target', [dupe, { ...dupe }, { name: 'Other', filePath: 'src/other.js' }], [], { maxRender: 2 });
  assert.match(dot, /"Caller" -> "Target"/);
  assert.match(dot, /"Other" -> "Target"/);
});

test('dedupeEdges collapses edges with the same from/to name and filePath', () => {
  const a = { name: 'A', filePath: 'a.js' };
  const b = { name: 'B', filePath: 'b.js' };
  const edges = dedupeEdges([{ from: a, to: b, depth: 2 }, { from: { ...a }, to: { ...b }, depth: 2 }]);
  assert.strictEqual(edges.length, 1);
});

test('dedupeEdges keeps edges that share a name but differ in filePath', () => {
  const edges = dedupeEdges([
    { from: { name: 'A', filePath: 'a.js' }, to: { name: 'B', filePath: 'b.js' }, depth: 2 },
    { from: { name: 'A', filePath: 'other.js' }, to: { name: 'B', filePath: 'b.js' }, depth: 2 },
  ]);
  assert.strictEqual(edges.length, 2);
});

test('depthColor fades lighter as depth increases and clamps at the last shade', () => {
  const two = depthColor(2);
  const three = depthColor(3);
  const deep = depthColor(50);
  assert.notStrictEqual(two, three);
  assert.strictEqual(deep, depthColor(4), 'expected very deep hops to clamp to the palette\'s last shade');
});

test('depthBudgetWarning fires only when traversal was truncated', () => {
  assert.strictEqual(depthBudgetWarning(false, 200), null);
  assert.match(depthBudgetWarning(true, 200), /safety cap of 200 discovered nodes/);
});

test('buildDot renders transitive (depth > 1) edges alongside direct ones', () => {
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'src/caller.js' }], [], {
    transitiveEdges: [{ from: { name: 'GrandCaller', filePath: 'src/gc.js' }, to: { name: 'Caller', filePath: 'src/caller.js' }, depth: 2 }],
  });
  assert.match(dot, /"Caller" -> "Target";/);
  assert.match(dot, /"GrandCaller" -> "Caller" \[color="#b8c2d0"\];/);
});

test('buildDot dashes transitive edges from test callers same as direct ones', () => {
  const dot = buildDot('Target', [], [], {
    transitiveEdges: [{ from: { name: 'HelperTest', filePath: 'src/helper.test.js' }, to: { name: 'Direct', filePath: 'src/direct.js' }, depth: 2 }],
  });
  assert.match(dot, /"HelperTest" -> "Direct" \[color="#b8c2d0", style=dashed, label="test"\];/);
});

test('allocateRenderBudget spends a shared allowance in priority order across dimensions', () => {
  assert.deepStrictEqual(allocateRenderBudget(5, [3, 4, 2]), [3, 2, 0]);
  assert.deepStrictEqual(allocateRenderBudget(20, [3, 4, 2]), [3, 4, 2], 'a budget larger than the total should not truncate anything');
  assert.deepStrictEqual(allocateRenderBudget(undefined, [3, 4, 2]), [3, 4, 2], 'no maxRender means no cap at all');
});

test('formatMismatchWarning fires when --out\'s extension is a real dot format that differs from --format', () => {
  assert.match(formatMismatchWarning('diagram.svg', 'png'), /--out ends in '\.svg' but --format is 'png'/);
  assert.strictEqual(formatMismatchWarning('diagram.svg', 'svg'), null, 'matching extension and format should not warn');
  assert.strictEqual(formatMismatchWarning('diagram.dot.bak', 'png'), null, 'an unrecognized extension should not false-positive');
  assert.strictEqual(formatMismatchWarning(null, 'png'), null, 'no --out (auto-generated path) can never mismatch');
});

test('matchSymbolNotFound extracts the symbol name from codegraph\'s plain-text not-found message', () => {
  assert.strictEqual(matchSymbolNotFound('[34mℹ[0m Symbol "Foo" not found\n'), 'Foo');
  assert.strictEqual(matchSymbolNotFound('{"symbol":"Foo","callers":[]}'), null, 'a real JSON response should never match');
});

test('matchNotInitialized detects codegraph\'s unindexed-repo message, not JSON or other errors', () => {
  const esc = String.fromCharCode(27);
  assert.strictEqual(matchNotInitialized(`${esc}[31m✗${esc}[0m CodeGraph not initialized in /repo\n Run "codegraph init" first`), true);
  assert.strictEqual(matchNotInitialized('codegraph not initialized'), true, 'case-insensitive');
  assert.strictEqual(matchNotInitialized('[{"node":{}}]'), false, 'a real JSON response must not match');
  assert.strictEqual(matchNotInitialized('Symbol "Foo" not found'), false, 'the not-found message is a different case');
  assert.strictEqual(matchNotInitialized(''), false);
});

test('argRepoPath recovers the --path value codeshot passed, defaulting to "."', () => {
  assert.strictEqual(argRepoPath(['query', '--path', '/repo/x', '--json', '--', 'Foo']), '/repo/x');
  assert.strictEqual(argRepoPath(['callers', '--json']), '.', 'no --path → cwd default');
});

test('parseCodegraphOutput does NOT treat a successful JSON response as "not initialized" just because a node\'s content mentions the phrase', () => {
  // Regression: codegraph's enumerate query returns indexed node content, and
  // this very file's source contains "CodeGraph not initialized" in a comment.
  // Scanning stdout for that phrase falsely reported a well-indexed repo as
  // uninitialized (the CI diagrams job caught it). The phrase is a real signal
  // only on stderr with a non-zero exit (runCodegraph), never on stdout.
  const stdout = JSON.stringify([{ node: { name: 'matchNotInitialized', content: 'detects "CodeGraph not initialized"' } }]);
  const parsed = parseCodegraphOutput(stdout, ['query', '--path', '.', '--json', '--', ''], { fatal: false });
  assert.deepStrictEqual(parsed, [{ node: { name: 'matchNotInitialized', content: 'detects "CodeGraph not initialized"' } }]);
});

test('buildDot styles a "kind":"file" caller/callee distinctly from a real function call', () => {
  const dot = buildDot('Target', [{ name: 'some.js', kind: 'file', filePath: 'src/some.js' }], [{ name: 'other.js', kind: 'file', filePath: 'src/other.js' }]);
  assert.match(dot, /"some\.js" -> "Target" \[style=dotted, color="#9ca3af", label="file"\];/);
  assert.match(dot, /"Target" -> "other\.js" \[style=dotted, color="#9ca3af", label="file"\];/);
});

test('buildDot\'s file-kind styling takes precedence over test-dash styling on the same node', () => {
  const dot = buildDot('Target', [{ name: 'weird.test.js', kind: 'file', filePath: 'src/weird.test.js' }], []);
  assert.match(dot, /"weird\.test\.js" -> "Target" \[style=dotted, color="#9ca3af", label="file"\];/);
  assert.doesNotMatch(dot, /label="test"/);
});

test('buildDot with no transitiveEdges option behaves exactly as before (backward compatible)', () => {
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'a.js' }], [{ name: 'Callee', filePath: 'b.js' }]);
  assert.doesNotMatch(dot, /color="#b8c2d0"/);
});

test('renderTruncationNote fires when distinct count exceeds maxRender', () => {
  assert.match(renderTruncationNote('callers', 10, 5), /rendering 5 of 10 distinct callers/);
});

test('renderTruncationNote is null when maxRender is unset or not exceeded', () => {
  assert.strictEqual(renderTruncationNote('callers', 10, undefined), null);
  assert.strictEqual(renderTruncationNote('callers', 5, 5), null);
  assert.strictEqual(renderTruncationNote('callers', 3, 5), null);
});

test('truncationWarning fires when results hit the limit', () => {
  const results = Array.from({ length: 20 }, (_, i) => ({ name: `Fn${i}` }));
  assert.match(truncationWarning('callers', results, 20), /showing 20 callers/);
});

test('truncationWarning is null when under the limit', () => {
  const results = Array.from({ length: 3 }, (_, i) => ({ name: `Fn${i}` }));
  assert.strictEqual(truncationWarning('callers', results, 20), null);
});

test('emptyGraphWarning fires only when a symbol has neither callers nor callees', () => {
  assert.match(emptyGraphWarning('Foo', [], []), /'Foo' has no callers or callees/);
  assert.strictEqual(emptyGraphWarning('Foo', [{ name: 'a' }], []), null);
  assert.strictEqual(emptyGraphWarning('Foo', [], [{ name: 'b' }]), null);
  // undefined arrays (codegraph returned nothing) count as empty, not a crash
  assert.match(emptyGraphWarning('Foo', undefined, undefined), /has no callers or callees/);
});

test('emptyArchitectureWarning fires only when there are zero cross-file edges', () => {
  assert.match(emptyArchitectureWarning([]), /no cross-file call edges — the diagram is blank/);
  assert.strictEqual(emptyArchitectureWarning([{ from: 'a', to: 'b', weight: 1 }]), null);
  assert.match(emptyArchitectureWarning(undefined), /no cross-file call edges/);
});

test('--limit rejects non-positive-integer values before reaching codegraph', () => {
  const { execFileSync } = require('child_process');
  for (const bad of ['abc', '0', '-5', '3.5', 'NaN']) {
    let threw = false;
    try {
      execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--limit', bad], { encoding: 'utf8', stdio: 'pipe' });
    } catch (err) {
      threw = true;
      assert.match(err.stderr, /--limit must be a positive integer/);
    }
    assert.strictEqual(threw, true, `expected --limit ${bad} to be rejected`);
  }
});

test('--max-render rejects non-positive-integer values before reaching codegraph', () => {
  const { execFileSync } = require('child_process');
  for (const bad of ['abc', '0', '-5', '3.5', 'NaN']) {
    let threw = false;
    try {
      execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--max-render', bad], { encoding: 'utf8', stdio: 'pipe' });
    } catch (err) {
      threw = true;
      assert.match(err.stderr, /--max-render must be a positive integer/);
    }
    assert.strictEqual(threw, true, `expected --max-render ${bad} to be rejected`);
  }
});

test('--depth rejects non-positive-integer values before reaching codegraph', () => {
  const { execFileSync } = require('child_process');
  for (const bad of ['abc', '0', '-5', '3.5', 'NaN']) {
    let threw = false;
    try {
      execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--depth', bad], { encoding: 'utf8', stdio: 'pipe' });
    } catch (err) {
      threw = true;
      assert.match(err.stderr, /--depth must be a positive integer/);
    }
    assert.strictEqual(threw, true, `expected --depth ${bad} to be rejected`);
  }
});

test('--max-depth-nodes rejects non-positive-integer values before reaching codegraph', () => {
  const { execFileSync } = require('child_process');
  for (const bad of ['abc', '0', '-5', '3.5', 'NaN']) {
    let threw = false;
    try {
      execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--max-depth-nodes', bad], { encoding: 'utf8', stdio: 'pipe' });
    } catch (err) {
      threw = true;
      assert.match(err.stderr, /--max-depth-nodes must be a positive integer/);
    }
    assert.strictEqual(threw, true, `expected --max-depth-nodes ${bad} to be rejected`);
  }
});

test('--architecture rejects an explicit --max-depth-nodes (no multi-hop file traversal to bound)', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), '--architecture', '--max-depth-nodes', '500'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--max-depth-nodes has no effect with --architecture/);
  }
  assert.strictEqual(threw, true, 'expected --architecture + --max-depth-nodes to be rejected');
});

test('--architecture accepts an explicit --max-depth-nodes that matches its own default (same non-divergence rule as --depth)', () => {
  // --depth 1 explicitly passed alongside --architecture is a silent no-op
  // because it matches --depth's own default (values.depth !== '1' in main).
  // --max-depth-nodes must follow the same rule for its own default (200), not
  // just "was --max-depth-nodes passed at all" — otherwise the two flags
  // disagree on what "explicitly passed" means for an identical "rejected only
  // if it would actually change anything" contract.
  const { execFileSync } = require('child_process');
  const path = require('path');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const out = execFileSync('node', [callgraphJs, '--architecture', '--max-depth-nodes', '200', '--path', repoRoot, '--format', 'dot'], { encoding: 'utf8', stdio: 'pipe' });
  assert.ok(out.trim().length > 0, 'expected --architecture --max-depth-nodes 200 to succeed and print an output path, not be rejected');
});

test('missing symbol argument is rejected with a codeshot-prefixed message', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js')], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /^codeshot: missing required <symbol> argument/);
  }
  assert.strictEqual(threw, true, 'expected missing symbol to be rejected');
});

test('explicit empty --out is rejected instead of silently falling back to a temp path', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--out='], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--out must not be empty/);
  }
  assert.strictEqual(threw, true, 'expected empty --out to be rejected');
});

test('explicit empty --path is rejected instead of silently falling through to codegraph', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--path='], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--path must not be empty/);
  }
  assert.strictEqual(threw, true, 'expected empty --path to be rejected');
});

test('explicit empty --format is rejected instead of silently defaulting to png', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--format='], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--format must not be empty/);
  }
  assert.strictEqual(threw, true, 'expected empty --format to be rejected');
});

test('buildDot output is valid DOT that the real `dot` binary accepts', () => {
  const { execFileSync } = require('child_process');
  const dot = buildDot('Weird "Name" \\ <html>', [
    { name: 'CallerTest', filePath: 'src/caller.test.js' },
    { name: 'Unicode λ Caller', filePath: 'src/caller2.js' },
  ], [
    { name: 'Callee', filePath: 'src/callee.js' },
  ]);
  let out;
  try {
    out = execFileSync('dot', ['-Tpng'], { input: dot });
  } catch (err) {
    if (err.code === 'ENOENT') throw new Error('`dot` not found on PATH — codeshot requires graphviz to run at all, see README.md#install');
    throw new Error(`dot rejected buildDot's output: ${err.stderr || err.message}`);
  }
  assert.ok(out.length > 0, 'expected dot to produce non-empty PNG output');
});

test('CLI runs end-to-end against this repo\'s own real codegraph index', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  // This repo is only self-indexed by `codegraph` on machines that have run
  // `codegraph init` against it (a dev-environment convenience, not something
  // a fresh clone or CI has) — skip rather than fail when that's not the case,
  // same as codeshot itself treats codegraph as an optional-at-test-time,
  // required-at-run-time external dependency.
  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const pngOut = path.join(os.tmpdir(), `codeshot-selftest-${Date.now()}.png`);
  const svgOut = path.join(os.tmpdir(), `codeshot-selftest-${Date.now()}.svg`);
  try {
    execFileSync('node', [callgraphJs, 'buildDot', '--path', repoRoot, '--out', pngOut], { encoding: 'utf8', stdio: 'pipe' });
    const png = fs.readFileSync(pngOut);
    assert.ok(png.length > 0 && png[0] === 0x89 && png.toString('ascii', 1, 4) === 'PNG', 'expected a real PNG file from the default format');

    execFileSync('node', [callgraphJs, 'buildDot', '--path', repoRoot, '--out', svgOut, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
    const svg = fs.readFileSync(svgOut, 'utf8');
    assert.match(svg, /<svg/, 'expected --format svg to produce real SVG output through the same pipeline');
  } finally {
    fs.rmSync(pngOut, { force: true });
    fs.rmSync(svgOut, { force: true });
  }
});

test('CLI --depth traversal runs end-to-end against this repo\'s own real codegraph index', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const depth1Out = path.join(os.tmpdir(), `codeshot-depth1-${Date.now()}.dot`);
  const depth2Out = path.join(os.tmpdir(), `codeshot-depth2-${Date.now()}.dot`);
  try {
    // --format dot renders the raw digraph text (no image encoding to compare
    // sizes on) so this can assert depth 2 discovers strictly more than depth 1
    // without depending on how graphviz happens to lay out a PNG/SVG.
    execFileSync('node', [callgraphJs, 'buildDot', '--path', repoRoot, '--out', depth1Out, '--format', 'dot'], { encoding: 'utf8', stdio: 'pipe' });
    execFileSync('node', [callgraphJs, 'buildDot', '--path', repoRoot, '--out', depth2Out, '--format', 'dot', '--depth', '2'], { encoding: 'utf8', stdio: 'pipe' });
    const depth1 = fs.readFileSync(depth1Out, 'utf8');
    const depth2 = fs.readFileSync(depth2Out, 'utf8');
    const countEdges = dot => (dot.match(/->/g) || []).length;
    assert.ok(countEdges(depth2) >= countEdges(depth1), 'expected --depth 2 to discover at least as many edges as depth 1');
  } finally {
    fs.rmSync(depth1Out, { force: true });
    fs.rmSync(depth2Out, { force: true });
  }
});

test('CLI --max-depth-nodes lowers the --depth traversal budget end-to-end against this repo\'s own real codegraph index', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const out = path.join(os.tmpdir(), `codeshot-maxdepthnodes-${Date.now()}.dot`);
  try {
    // buildDot has at least one real caller/callee in this repo, so seeding the
    // depth-2 traversal's discovered set already meets a budget of 1 — the
    // traversal must report it hit the (lowered, not default 200) cap.
    const { spawnSync } = require('child_process');
    const result = spawnSync('node', [callgraphJs, 'buildDot', '--path', repoRoot, '--out', out, '--format', 'dot', '--depth', '2', '--max-depth-nodes', '1'], { encoding: 'utf8' });
    assert.strictEqual(result.status, 0, `expected a successful render even when the depth budget is hit, got stderr: ${result.stderr}`);
    assert.match(result.stderr, /safety cap of 1 discovered nodes/, 'expected the lowered --max-depth-nodes value to appear in the truncation warning');
  } finally {
    fs.rmSync(out, { force: true });
  }
});

test('CLI resolves a fuzzy/partial query to its canonical name for the rendered root label', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['query', '--path', repoRoot, '--json', '--limit', '1', '--', 'buildD'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const out = path.join(os.tmpdir(), `codeshot-resolve-${Date.now()}.dot`);
  try {
    // 'buildD' is a deliberate partial query -- codegraph fuzzy-resolves it to
    // the real symbol 'buildDot'. The rendered root label must show the
    // resolved canonical name, not the literal query string.
    execFileSync('node', [callgraphJs, 'buildD', '--path', repoRoot, '--out', out, '--format', 'dot'], { encoding: 'utf8', stdio: 'pipe' });
    const dot = fs.readFileSync(out, 'utf8');
    // `dot -Tdot` reserializes and sorts node attributes alphabetically, so
    // fillcolor is no longer guaranteed to be the first attribute on the node —
    // match it anywhere within the buildDot node's attribute list.
    assert.match(dot, /\bbuildDot\b\s*\[[^\]]*fillcolor="#e2e8f0"/, 'expected the root node to be labeled with the resolved name "buildDot", not the raw query "buildD"');
  } finally {
    fs.rmSync(out, { force: true });
  }
});

// --- --architecture mode ---------------------------------------------

test('unwrapQueryNodes unwraps .node and keeps kind:file entries (probed for anonymous-callback calls)', () => {
  const results = [
    { node: { name: 'Foo', kind: 'function', filePath: 'a.js' } },
    { node: { name: 'a.js', kind: 'file', filePath: 'a.js' } },
    { node: { name: 'BAR', kind: 'constant', filePath: 'b.js' } },
    { node: null },
    { node: { name: '', kind: 'function', filePath: 'c.js' } },
    { node: { name: undefined, kind: 'function', filePath: 'd.js' } },
  ];
  const symbols = unwrapQueryNodes(results);
  assert.strictEqual(symbols.length, 3, 'a missing/empty name must be dropped, not passed through to a codegraph subprocess call');
  assert.deepStrictEqual(symbols.map(s => s.name), ['Foo', 'a.js', 'BAR']);
});

test('symbolBudgetWarning fires only when enumeration was truncated', () => {
  assert.strictEqual(symbolBudgetWarning(false, 500), null);
  assert.match(symbolBudgetWarning(true, 500), /stopped enumerating after 500 symbols/);
});

test('duplicateNameWarning fires when a name appears in more than one file', () => {
  const symbols = [
    { name: 'render', filePath: 'a.js' },
    { name: 'render', filePath: 'b.js' },
    { name: 'unique', filePath: 'c.js' },
  ];
  assert.match(duplicateNameWarning(symbols), /render/);
});

test('duplicateNameWarning is null when every name is unique', () => {
  const symbols = [{ name: 'A', filePath: 'a.js' }, { name: 'B', filePath: 'b.js' }];
  assert.strictEqual(duplicateNameWarning(symbols), null);
});

test('duplicateNameWarning reports ordinary duplicates as re-probed, not as possibly-wrong', () => {
  const symbols = [
    { name: 'render', kind: 'function', filePath: 'a.js' },
    { name: 'render', kind: 'function', filePath: 'b.js' },
  ];
  const warning = duplicateNameWarning(symbols);
  assert.match(warning, /file-qualified/);
  assert.doesNotMatch(warning, /may be attributed to the wrong file/);
});

test('duplicateNameWarning still warns about duplicate file names, which node -f cannot disambiguate', () => {
  const symbols = [
    { name: 'index.js', kind: 'file', filePath: 'a/index.js' },
    { name: 'index.js', kind: 'file', filePath: 'b/index.js' },
  ];
  const warning = duplicateNameWarning(symbols);
  assert.match(warning, /may be attributed to the wrong file/);
  assert.match(warning, /index\.js/);
});

test('duplicateNames returns only names seen in more than one file', () => {
  const dupes = duplicateNames([
    { name: 'render', filePath: 'a.js' },
    { name: 'render', filePath: 'b.js' },
    { name: 'unique', filePath: 'c.js' },
  ]);
  assert.deepStrictEqual([...dupes], ['render']);
});

test('duplicateNames ignores same-name-same-file symbols, which have no file ambiguity to resolve', () => {
  // Two methods named String() on different types in one file: the bare-name
  // probe's union of their callees is already exactly right for that file, and
  // re-probing by file would only lose edges.
  const dupes = duplicateNames([
    { name: 'String', filePath: 'svc.go' },
    { name: 'String', filePath: 'svc.go' },
  ]);
  assert.deepStrictEqual([...dupes], []);
});

// Fixtures below are real `codegraph node -f` output shapes from the pinned
// 1.5.0 — the whole point of parseNodeCalls is that it reads text, not JSON, so
// these pin the format the parser was written against.
const NODE_OUTPUT_HEAD = [
  '**buildDot** (function)',
  '',
  '**Location:** render/callgraph.js:314',
  '**Signature:** `(symbol, callers = [], callees = [])`',
  '',
];

const TRAIL_HEADER = '**Trail — codegraph_node any of these to follow it (no Read needed)**';

test('parseNodeCalls reads file-qualified callees out of a real node -f trail', () => {
  const out = [
    ...NODE_OUTPUT_HEAD,
    TRAIL_HEADER,
    '**Calls →** dedupeNodes (render/callgraph.js:223), depthColor (render/other.js:251)',
    '**Called by ←** main (render/callgraph.js:821)',
  ].join('\n');
  assert.deepStrictEqual(parseNodeCalls(out, 'render/callgraph.js'), [
    { name: 'dedupeNodes', filePath: 'render/callgraph.js' },
    { name: 'depthColor', filePath: 'render/other.js' },
  ]);
});

test('parseNodeCalls returns [] — not null — for a recognized symbol that calls nothing', () => {
  const out = [...NODE_OUTPUT_HEAD, TRAIL_HEADER, '**Called by ←** main (render/callgraph.js:821)'].join('\n');
  assert.deepStrictEqual(parseNodeCalls(out, 'render/callgraph.js'), []);
});

test('parseNodeCalls returns null on unrecognized output so the caller falls back instead of inventing an empty result', () => {
  for (const bad of ['', 'Symbol "X" not found in the codebase', '{"callees":[]}', 'total garbage']) {
    assert.strictEqual(parseNodeCalls(bad, 'a.js'), null, `expected null for ${JSON.stringify(bad)}`);
  }
});

test('parseNodeCalls returns null for file-mode output, which has a header and a location but no trail', () => {
  // `codegraph node -f <file> <basename>` answers in file mode. Returning []
  // here would read as "this file calls nothing" and suppress the fallback.
  const out = ['**callgraph.js** (file)', '', '**Location:** render/callgraph.js:1', '', '```javascript', "1\t#!/usr/bin/env node", '```'].join('\n');
  assert.strictEqual(parseNodeCalls(out, 'render/callgraph.js'), null);
});

test('parseNodeCalls returns null when codegraph truncates the trail with "+N more"', () => {
  // Measured on codegraph 1.5.0: the trail line caps at 12 entries. `main` in
  // this repo has 23 callees and its trail shows 12 + "+11 more". Taking the
  // visible 12 would silently drop real edges — worse than the over-reporting
  // bare-name probe we fall back to.
  const out = [
    ...NODE_OUTPUT_HEAD,
    TRAIL_HEADER,
    '**Calls →** a (x.js:1), b (x.js:2), c (x.js:3), +11 more',
  ].join('\n');
  assert.strictEqual(parseNodeCalls(out, 'render/callgraph.js'), null);
});

test('parseNodeCalls returns null when the answer is for a different file than the one probed', () => {
  // `-f` is a preference, not a filter: codegraph answers with another file's
  // same-named symbol (exit 0, no error) when the requested file has no match.
  const out = [...NODE_OUTPUT_HEAD, TRAIL_HEADER, '**Calls →** alpha (a/alpha.js:1)'].join('\n');
  assert.strictEqual(parseNodeCalls(out, 'b/svc.js'), null);
  assert.deepStrictEqual(parseNodeCalls(out, 'render/callgraph.js'), [{ name: 'alpha', filePath: 'a/alpha.js' }]);
});

test('parseNodeCalls returns null when codegraph concatenates more than one matching symbol', () => {
  // Two same-named symbols in one file come back as two trail blocks; keeping
  // only one would silently drop the other's edges.
  const out = [
    ...NODE_OUTPUT_HEAD,
    TRAIL_HEADER,
    '**Calls →** alpha (a/alpha.js:1)',
    '',
    '**handle** (function)',
    '',
    '**Location:** render/callgraph.js:400',
    TRAIL_HEADER,
    '**Calls →** beta (b/beta.js:1)',
  ].join('\n');
  assert.strictEqual(parseNodeCalls(out, 'render/callgraph.js'), null);
});

test('parseNodeCalls drops a file-node callee, which has no real call site to draw', () => {
  const out = [
    ...NODE_OUTPUT_HEAD,
    TRAIL_HEADER,
    '**Calls →** run.js (test/run.js:1), dedupeNodes (render/callgraph.js:223)',
  ].join('\n');
  assert.deepStrictEqual(parseNodeCalls(out, 'render/callgraph.js'), [{ name: 'dedupeNodes', filePath: 'render/callgraph.js' }]);
});

test('parseNodeCalls ignores a trail-shaped line inside the embedded source body', () => {
  // node -f echoes the symbol's own source, and this repo's source legitimately
  // contains lines describing the trail format.
  const out = [
    ...NODE_OUTPUT_HEAD,
    '```javascript',
    '315\t// the Calls line, e.g. dedupeNodes (render/callgraph.js:223)',
    '```',
    TRAIL_HEADER,
    '**Calls →** dedupeNodes (render/callgraph.js:223)',
  ].join('\n');
  assert.deepStrictEqual(parseNodeCalls(out, 'render/callgraph.js'), [{ name: 'dedupeNodes', filePath: 'render/callgraph.js' }]);
});

test('parseCodegraphOutput does not mistake indexed source content for a not-found message', () => {
  // Under json:false the response embeds arbitrary source, and this repo's own
  // test file contains the literal not-found sentence. Matching it anywhere in
  // the body would null out a perfectly good answer and silently fall back.
  const out = [
    '**probe** (function)',
    '',
    '**Location:** test/run.js:700',
    '',
    '```javascript',
    '700\tassert.match(err, /Symbol "Foo" not found in the codebase/);',
    '```',
    TRAIL_HEADER,
    '**Calls →** assert (test/run.js:4)',
  ].join('\n');
  assert.strictEqual(parseCodegraphOutput(out, ['node'], { fatal: false, json: false }), out);
});

test('aggregateFileEdges drops self-file edges', () => {
  const edges = aggregateFileEdges([{ fromFile: 'a.js', toFile: 'a.js' }, { fromFile: 'a.js', toFile: 'b.js' }]);
  assert.strictEqual(edges.length, 1);
  assert.strictEqual(edges[0].to, 'b.js');
});

test('aggregateFileEdges drops edges missing a real from/to filePath', () => {
  const edges = aggregateFileEdges([
    { fromFile: 'a.js', toFile: '' },
    { fromFile: '', toFile: 'b.js' },
    { fromFile: 'a.js', toFile: 'b.js' },
  ]);
  assert.strictEqual(edges.length, 1);
});

test('aggregateFileEdges sums repeated pairs into edge weight', () => {
  const edges = aggregateFileEdges([
    { fromFile: 'a.js', toFile: 'b.js' },
    { fromFile: 'a.js', toFile: 'b.js' },
    { fromFile: 'a.js', toFile: 'b.js' },
  ]);
  assert.strictEqual(edges.length, 1);
  assert.strictEqual(edges[0].weight, 3);
});

test('topFilesByWeight returns null (no cap) when maxRender is unset', () => {
  const edges = aggregateFileEdges([{ fromFile: 'a.js', toFile: 'b.js' }]);
  assert.strictEqual(topFilesByWeight(edges, undefined), null);
});

test('topFilesByWeight keeps only the top-N busiest files', () => {
  const edges = [
    { from: 'busy.js', to: 'a.js', weight: 5 },
    { from: 'busy.js', to: 'b.js', weight: 5 },
    { from: 'quiet.js', to: 'c.js', weight: 1 },
  ];
  const kept = topFilesByWeight(edges, 1);
  assert.strictEqual(kept.size, 1);
  assert.ok(kept.has('busy.js'));
});

test('buildArchitectureDot renders a file node per endpoint and a weighted edge', () => {
  const dot = buildArchitectureDot([{ from: 'src/a.js', to: 'src/b.js', weight: 2 }]);
  assert.match(dot, /^digraph architecture \{/);
  assert.match(dot, /"src\/a\.js";/);
  assert.match(dot, /"src\/b\.js";/);
  assert.match(dot, /"src\/a\.js" -> "src\/b\.js" \[label="2"\];/);
});

test('buildArchitectureDot dashes file nodes that look like test files', () => {
  const dot = buildArchitectureDot([{ from: 'test/a.spec.js', to: 'src/b.js', weight: 1 }]);
  assert.match(dot, /"test\/a\.spec\.js" \[style="rounded,filled,dashed"\];/);
});

test('buildArchitectureDot respects maxRender by dropping edges outside the top-N files', () => {
  const edges = [
    { from: 'busy.js', to: 'a.js', weight: 5 },
    { from: 'quiet.js', to: 'c.js', weight: 1 },
  ];
  const dot = buildArchitectureDot(edges, { maxRender: 2 });
  assert.match(dot, /"busy\.js" -> "a\.js"/);
  assert.doesNotMatch(dot, /"quiet\.js" -> "c\.js"/);
});

test('architectureOutputBaseName sanitizes a repo path down to its basename', () => {
  assert.strictEqual(architectureOutputBaseName('/home/ericm/personal_projects/codeshot/master'), 'master');
});

// --- --group-depth: rolling the file graph up into directory groups ------

test('groupPath rolls a nested file up to its first N directory segments', () => {
  assert.strictEqual(groupPath('src/api/user.js', 1), 'src/');
  assert.strictEqual(groupPath('src/api/user.js', 2), 'src/api/');
});

test('groupPath leaves a repo-root file as itself (no directory to roll into)', () => {
  // Grouping it under '' would merge every root-level file into one nameless box.
  assert.strictEqual(groupPath('index.js', 1), 'index.js');
});

test('groupPath degrades to the file\'s own directories when depth exceeds the tree', () => {
  assert.strictEqual(groupPath('render/callgraph.js', 5), 'render/');
});

test('groupPath splits Windows separators too', () => {
  assert.strictEqual(groupPath('src\\api\\user.js', 1), 'src/');
});

test('rollupFileEdges sums the weights of every file pair collapsing into one group pair', () => {
  const rolled = rollupFileEdges([
    { from: 'src/a.js', to: 'lib/x.js', weight: 2 },
    { from: 'src/b.js', to: 'lib/y.js', weight: 3 },
  ], 1);
  assert.deepStrictEqual(rolled, [{ from: 'src/', to: 'lib/', weight: 5 }]);
});

test('rollupFileEdges drops edges that become self-group (intra-module calls)', () => {
  const rolled = rollupFileEdges([
    { from: 'src/a.js', to: 'src/b.js', weight: 4 },
    { from: 'src/a.js', to: 'lib/x.js', weight: 1 },
  ], 1);
  assert.deepStrictEqual(rolled, [{ from: 'src/', to: 'lib/', weight: 1 }]);
});

test('rollupFileEdges is a no-op when no --group-depth was given', () => {
  const edges = [{ from: 'src/a.js', to: 'src/b.js', weight: 1 }];
  assert.strictEqual(rollupFileEdges(edges, undefined), edges);
});

test('groupCollapseWarning fires only when the rollup ate every edge', () => {
  assert.match(groupCollapseWarning(7, 0, 1, 1), /--group-depth 1 left no edges to draw — all 7 cross-file edge\(s\)/);
  assert.strictEqual(groupCollapseWarning(7, 3, 1, 2), null, 'edges survived — not a collapse');
  assert.strictEqual(groupCollapseWarning(0, 0, 1, 0), null, 'nothing to collapse — emptyArchitectureWarning owns this case');
  assert.strictEqual(groupCollapseWarning(7, 0, undefined, 0), null, 'no --group-depth — not the flag\'s doing');
});

test('groupCollapseWarning gives opposite advice for one group vs several', () => {
  // One group: the depth is too coarse, going deeper helps.
  assert.match(groupCollapseWarning(3, 0, 1, 1), /every file falls into a single group.*Try a deeper --group-depth/s);
  // Several groups: the modules genuinely don't call each other, so a deeper
  // --group-depth stays blank — telling the user to go deeper would be a dead end.
  const many = groupCollapseWarning(3, 0, 1, 2);
  assert.match(many, /the 2 groups at this depth have no calls between them/);
  assert.match(many, /a deeper --group-depth will stay blank/);
  assert.doesNotMatch(many, /within one directory/, 'must not assert a single directory when there are several');
});

test('sortSymbolsForEnumeration compares by code point, not locale collation', () => {
  // localeCompare with no explicit locale uses the implementation-default
  // locale, which varies with the environment and the Node binary's ICU build —
  // that would reintroduce the run-to-run variance this sort exists to remove.
  // Code point order puts uppercase before lowercase and '_' (U+005F) after
  // uppercase; en-US collation does neither.
  const sorted = sortSymbolsForEnumeration([
    { name: 'a', filePath: 'src/api.js' },
    { name: 'a', filePath: 'src/Api.js' },
    { name: 'a', filePath: 'src/_x.js' },
  ]);
  assert.deepStrictEqual(sorted.map(s => s.filePath), ['src/Api.js', 'src/_x.js', 'src/api.js']);
});

test('symbolBudgetWarning names the cut as a path-sorted prefix, not a sample', () => {
  // Otherwise "the graph is incomplete" reads as "a few edges missing", hiding
  // that unprobed late-path files render as sinks that appear to call nothing.
  const warning = symbolBudgetWarning(true, 500);
  assert.match(warning, /path-sorted prefix, not a sample/);
  assert.match(warning, /may appear to call nothing when they do/);
});

test('buildArchitectureDot still dashes a rolled-up test directory group', () => {
  // isTestRef reads path segments, so the group's trailing slash must not defeat it.
  const dot = buildArchitectureDot(rollupFileEdges([{ from: 'test/run.js', to: 'render/callgraph.js', weight: 1 }], 1));
  assert.match(dot, /"test\/"\s*\[style="rounded,filled,dashed"\]/);
});

test('sortSymbolsForEnumeration orders by filePath then name, so a --max-symbols slice is reproducible', () => {
  const sorted = sortSymbolsForEnumeration([
    { name: 'zeta', filePath: 'b.js' },
    { name: 'beta', filePath: 'a.js' },
    { name: 'alpha', filePath: 'a.js' },
  ]);
  assert.deepStrictEqual(sorted.map(s => `${s.filePath}:${s.name}`), ['a.js:alpha', 'a.js:beta', 'b.js:zeta']);
});

test('sortSymbolsForEnumeration does not mutate its input', () => {
  const input = [{ name: 'z', filePath: 'b.js' }, { name: 'a', filePath: 'a.js' }];
  sortSymbolsForEnumeration(input);
  assert.strictEqual(input[0].name, 'z');
});

test('--group-depth is rejected outside --architecture (no file graph to roll up)', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--group-depth', '1'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--group-depth only applies with --architecture/);
  }
  assert.strictEqual(threw, true, 'expected --group-depth without --architecture to be rejected');
});

test('--group-depth is rejected under --diff with a --diff-specific reason, not symbol mode\'s "symbol trail" wording', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), '--diff', '--group-depth', '1'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--group-depth only applies with --architecture/);
    assert.match(err.stderr, /--diff diagrams individual changed symbols/);
    assert.doesNotMatch(err.stderr, /a symbol trail has no file-level graph/);
  }
  assert.strictEqual(threw, true, 'expected --group-depth under --diff to be rejected');
});

test('--group-depth rejects a non-positive-integer value', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), '--architecture', '--group-depth', '0'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--group-depth must be a positive integer, got '0'/);
  }
  assert.strictEqual(threw, true, 'expected --group-depth 0 to be rejected');
});

test('CLI --architecture --group-depth 1 draws directory groups, not files', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  // --format dot prints the DOT source itself, so the node ids are directly
  // assertable — this repo's two source files live in render/ and test/.
  const out = execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--group-depth', '1', '--format', 'dot'], { encoding: 'utf8', stdio: 'pipe' });
  const dot = require('fs').readFileSync(out.trim(), 'utf8');
  try {
    assert.match(dot, /"render\/"/, 'expected a render/ group node');
    assert.match(dot, /"test\/"/, 'expected a test/ group node');
    assert.doesNotMatch(dot, /callgraph\.js/, 'files must be rolled up, not drawn alongside their groups');
  } finally {
    require('fs').rmSync(out.trim(), { force: true });
  }
});

test('CLI --embed --architecture --group-depth writes its own marker block, leaving an ungrouped one intact', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-groupembed-'));
  const doc = path.join(dir, 'DOC.md');
  fs.writeFileSync(doc, '# Doc\n\n<!-- codeshot:arch:start -->\n![existing](existing.svg)\n<!-- codeshot:arch:end -->\n', 'utf8');
  try {
    execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--group-depth', '1', '--embed', doc, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
    const md = fs.readFileSync(doc, 'utf8');
    assert.match(md, /<!-- codeshot:arch-d1:start -->/, 'grouped view must use its own marker id');
    assert.match(md, /!\[existing\]\(existing\.svg\)/, 'the pre-existing ungrouped block must be left alone');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('CLI --architecture runs end-to-end against this repo\'s own real codegraph index', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const out = path.join(os.tmpdir(), `codeshot-arch-selftest-${Date.now()}.svg`);
  try {
    execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--out', out, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
    const svg = fs.readFileSync(out, 'utf8');
    assert.match(svg, /<svg/, 'expected --architecture to produce real SVG output');
    // This repo has exactly two files (render/callgraph.js, test/run.js) that call
    // into each other (test/run.js requires callgraph.js's exports) — the rendered
    // graph should show at least one real cross-file edge, not an empty graph.
    assert.match(svg, /callgraph\.js/);
  } finally {
    fs.rmSync(out, { force: true });
  }
});

// The regression test for the duplicate-name fix. This repo's own index has zero
// duplicate names, so the self-test above can never exercise the file-qualified
// path — it needs a purpose-built repo where the bare-name probe is provably
// wrong. Measured against the real codegraph 1.5.0: `codegraph callees handle`
// returns the UNION of both files' callees, so the pre-fix code drew 4 edges of
// which 2 were fabricated. Building the fixture index takes ~2s.
test('--architecture attributes a duplicate-named symbol\'s edges to its own file, not the union of every same-named symbol', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const callgraphJs = path.join(__dirname, '..', 'render', 'callgraph.js');

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-dupname-'));
  try {
    fs.mkdirSync(path.join(dir, 'a'));
    fs.mkdirSync(path.join(dir, 'b'));
    // Two files defining `handle`, each calling a DIFFERENT function. The bare
    // name alone cannot tell them apart; the containing file can.
    fs.writeFileSync(path.join(dir, 'a', 'svc.js'), 'const { alpha } = require("./alpha");\nfunction handle() { return alpha(); }\nmodule.exports = { handle };\n');
    fs.writeFileSync(path.join(dir, 'a', 'alpha.js'), 'function alpha() { return 1; }\nmodule.exports = { alpha };\n');
    fs.writeFileSync(path.join(dir, 'b', 'svc.js'), 'const { beta } = require("../b/beta");\nfunction handle() { return beta(); }\nmodule.exports = { handle };\n');
    fs.writeFileSync(path.join(dir, 'b', 'beta.js'), 'function beta() { return 2; }\nmodule.exports = { beta };\n');
    // Two `run` methods in ONE file, calling different things. Same name, but no
    // file ambiguity — so this must keep the bare-name probe and keep BOTH edges.
    // Routing it through the file-qualified probe loses one: codegraph answers a
    // same-file collision with two concatenated trail blocks.
    fs.mkdirSync(path.join(dir, 'c'));
    fs.writeFileSync(path.join(dir, 'c', 'dual.js'), 'const { alpha } = require("../a/alpha");\nconst { beta } = require("../b/beta");\nclass A { run() { return alpha(); } }\nclass B { run() { return beta(); } }\nmodule.exports = { A, B };\n');

    try {
      execFileSync('codegraph', ['init', dir], { stdio: 'pipe', timeout: 180000 });
    } catch {
      console.log('  # skipped: `codegraph` not on PATH or could not index the fixture repo');
      return;
    }

    const out = path.join(dir, 'arch.dot');
    execFileSync('node', [callgraphJs, '--architecture', '--path', dir, '--out', out, '--format', 'dot'], { encoding: 'utf8', stdio: 'pipe', timeout: 180000 });
    const dot = fs.readFileSync(out, 'utf8');

    assert.match(dot, /"a\/svc\.js" -> "a\/alpha\.js"/, 'expected the real edge from a/svc.js');
    assert.match(dot, /"b\/svc\.js" -> "b\/beta\.js"/, 'expected the real edge from b/svc.js');
    assert.doesNotMatch(dot, /"a\/svc\.js" -> "b\/beta\.js"/, 'a/svc.js does not call beta — this is the misattributed edge the fix removes');
    assert.doesNotMatch(dot, /"b\/svc\.js" -> "a\/alpha\.js"/, 'b/svc.js does not call alpha — this is the misattributed edge the fix removes');
    assert.match(dot, /"c\/dual\.js" -> "a\/alpha\.js"/, 'same-file duplicates must keep both edges, not just the last trail block');
    assert.match(dot, /"c\/dual\.js" -> "b\/beta\.js"/, 'same-file duplicates must keep both edges, not just the last trail block');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('--architecture rejects a <symbol> argument', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--architecture'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--architecture cannot be combined with a <symbol> argument/);
  }
  assert.strictEqual(threw, true, 'expected --architecture + <symbol> to be rejected');
});

test('--architecture rejects --depth > 1', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), '--architecture', '--depth', '2'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--depth has no effect with --architecture/);
  }
  assert.strictEqual(threw, true, 'expected --architecture + --depth 2 to be rejected');
});

test('CLI --diff runs end-to-end against this repo\'s own real codegraph index and git history', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const out = path.join(os.tmpdir(), `codeshot-diff-selftest-${Date.now()}.svg`);
  try {
    // --diff-ref HEAD~1 means `git diff HEAD~1` — working tree vs that
    // commit, NOT a two-commit HEAD~1-vs-HEAD comparison, so this is NOT
    // fully independent of the ambient working tree (uncommitted changes,
    // if any, are included on top). What IS guaranteed regardless of that
    // state: HEAD~1..HEAD always touched render/callgraph.js in this repo's
    // real history, so the committed diff alone is enough for the
    // assertions below (non-blank SVG, at least one bold root) to hold —
    // they don't depend on exactly what's staged/unstaged, just that it's
    // never LESS than that committed floor. Also exercises the actual
    // `git diff --name-only -z --end-of-options <ref>` invocation
    // end-to-end, not just buildDiffDot's pure rendering.
    execFileSync('node', [callgraphJs, '--diff', '--diff-ref', 'HEAD~1', '--path', repoRoot, '--out', out, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
    const svg = fs.readFileSync(out, 'utf8');
    assert.match(svg, /<svg/, 'expected --diff to produce real SVG output');
    // Graphviz renders a bolded root as font-weight="bold" in SVG (DOT's
    // fontname="Helvetica-Bold" doesn't appear literally) — confirms at
    // least one changed symbol was actually drawn as a root, not a blank graph.
    assert.match(svg, /font-weight="bold"/);
  } finally {
    fs.rmSync(out, { force: true });
  }
});

test('--diff rejects a <symbol> argument', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--diff'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--diff cannot be combined with a <symbol> argument/);
  }
  assert.strictEqual(threw, true, 'expected --diff + <symbol> to be rejected');
});

test('--diff rejects --architecture', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), '--diff', '--architecture'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--architecture cannot be combined with --diff/);
  }
  assert.strictEqual(threw, true, 'expected --diff + --architecture to be rejected');
});

// --- --embed / --check ------------------------------------------------

test('embedMarkers keys start/end comments by the marker id', () => {
  assert.deepStrictEqual(embedMarkers('arch'), {
    start: '<!-- codeshot:arch:start -->',
    end: '<!-- codeshot:arch:end -->',
  });
});

test('applyEmbed appends a fresh block after a blank line when no markers exist', () => {
  const out = applyEmbed('# Title\n\nSome prose.\n', 'arch', '![x](a.svg)');
  assert.strictEqual(out, '# Title\n\nSome prose.\n\n<!-- codeshot:arch:start -->\n![x](a.svg)\n<!-- codeshot:arch:end -->\n');
});

test('applyEmbed replaces the existing block in place (idempotent on re-run)', () => {
  const base = 'intro\n\n<!-- codeshot:arch:start -->\n![old](old.svg)\n<!-- codeshot:arch:end -->\n\noutro\n';
  const once = applyEmbed(base, 'arch', '![new](new.svg)');
  assert.match(once, /!\[new\]\(new\.svg\)/);
  assert.doesNotMatch(once, /old\.svg/);
  assert.match(once, /^intro\n/);
  assert.match(once, /outro\n$/);
  // re-applying the same markdown is a no-op
  assert.strictEqual(applyEmbed(once, 'arch', '![new](new.svg)'), once);
});

test('applyEmbed keeps two different embed ids independent in one doc', () => {
  let doc = '# Doc\n';
  doc = applyEmbed(doc, 'arch', '![arch](arch.svg)');
  doc = applyEmbed(doc, 'buildDot', '![bd](bd.svg)');
  assert.match(doc, /codeshot:arch:start/);
  assert.match(doc, /codeshot:buildDot:start/);
  // updating arch must not touch the buildDot block
  const updated = applyEmbed(doc, 'arch', '![arch2](arch2.svg)');
  assert.match(updated, /arch2\.svg/);
  assert.match(updated, /bd\.svg/);
});

test('applyEmbed throws on a malformed (lone) marker rather than mangling the doc', () => {
  assert.throws(() => applyEmbed('x\n<!-- codeshot:arch:start -->\ny\n', 'arch'), /malformed/);
});

test('applyEmbed treats a "$" in the markdown as a literal, not a regex replacement token', () => {
  const base = '<!-- codeshot:arch:start -->\nold\n<!-- codeshot:arch:end -->\n';
  const out = applyEmbed(base, 'arch', '![a]($1 price.svg)');
  assert.match(out, /\$1 price\.svg/);
});

test('embedRelLink resolves the image path relative to the doc, forward-slashed', () => {
  assert.strictEqual(embedRelLink('/repo/TECHNICAL.md', '/repo/docs/arch.svg'), 'docs/arch.svg');
  assert.strictEqual(embedRelLink('/repo/docs/TECHNICAL.md', '/repo/docs/arch.svg'), 'arch.svg');
});

test('CLI --check without --embed is rejected', () => {
  const { execFileSync } = require('child_process');
  let threw = false;
  try {
    execFileSync('node', [require('path').join(__dirname, '..', 'render', 'callgraph.js'), 'Foo', '--check'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /--check only applies with --embed/);
  }
  assert.strictEqual(threw, true, 'expected --check without --embed to be rejected');
});

test('CLI --diff --embed on an empty diff refuses to overwrite an existing embedded diagram', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    // main() checks codegraph/dot are on PATH before runDiffMode even sees
    // the empty diff, regardless of whether this path ends up calling
    // codegraph — so this test needs the same portability guard as the
    // other CLI tests, or it fails on the wrong error on a machine without
    // codegraph installed.
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-diff-embed-refusal-'));
  const doc = path.join(dir, 'DOC.md');
  const original = '# Doc\n\n<!-- codeshot:diff:start -->\n![pre-existing](pre-existing.svg)\n<!-- codeshot:diff:end -->\n';
  fs.writeFileSync(doc, original, 'utf8');
  let threw = false;
  try {
    // HEAD..HEAD is guaranteed empty regardless of repo/working-tree state.
    execFileSync('node', [callgraphJs, '--diff', '--diff-ref', 'HEAD..HEAD', '--path', repoRoot, '--embed', doc, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /refusing to overwrite the existing diagram embedded in/);
  }
  assert.strictEqual(threw, true, 'expected an empty --diff --embed to be refused, not silently blank the doc');
  // The doc itself must be untouched — this is the actual data-loss guard,
  // not just that the process exited non-zero.
  assert.strictEqual(fs.readFileSync(doc, 'utf8'), original);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('CLI --diff --embed --check on an empty diff reports "nothing to check" and exits 0 — not the embed refusal, and not a false drift report (regression: --check had no reachable passing state on a zero-root diff)', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-diff-embed-check-'));
  const doc = path.join(dir, 'DOC.md');
  // Deliberately stale relative to what a blank render's markdown would
  // look like — proves --check isn't comparing at all, not just that it
  // happens to compare-and-pass: a zero-root diff has nothing this
  // invocation would diagram, so the committed block (from some other
  // invocation/range) isn't "drift" to report either way.
  const original = '# Doc\n\n<!-- codeshot:diff:start -->\nstale\n<!-- codeshot:diff:end -->\n';
  fs.writeFileSync(doc, original, 'utf8');
  const stdout = execFileSync('node', [callgraphJs, '--diff', '--diff-ref', 'HEAD..HEAD', '--path', repoRoot, '--embed', doc, '--check', '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
  assert.match(stdout, /nothing to diagram, so nothing to check/);
  assert.doesNotMatch(stdout, /refusing to overwrite/);
  assert.doesNotMatch(stdout, /out of date/);
  // Never writes, on this path either.
  assert.strictEqual(fs.readFileSync(doc, 'utf8'), original);
  fs.rmSync(dir, { recursive: true, force: true });
});

test('CLI --embed --architecture round-trips: writes image + block, --check then passes, drift then fails', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const repoRoot = path.join(__dirname, '..');
  const callgraphJs = path.join(repoRoot, 'render', 'callgraph.js');

  try {
    execFileSync('codegraph', ['callers', '--path', repoRoot, '--limit', '1', '--json', '--', 'buildDot'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH or this repo is not codegraph-indexed');
    return;
  }

  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-embed-'));
  const doc = path.join(dir, 'TECHNICAL.md');
  fs.writeFileSync(doc, '# Technical\n\nProse.\n', 'utf8');
  try {
    // 1) embed: writes the image next to the doc and inserts the block
    execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--embed', doc, '--format', 'svg'], { encoding: 'utf8', stdio: 'pipe' });
    const md = fs.readFileSync(doc, 'utf8');
    assert.match(md, /<!-- codeshot:arch:start -->/);
    assert.match(md, /!\[[^\]]*generated by codeshot\]\(codeshot-arch-[^)]+\.svg\)/);
    // The arch alt must be path-independent (not the checkout dir basename), so
    // the embedded markdown is reproducible across clones and --check is stable.
    // Before the fix it was `<basename(repoRoot)> architecture — ...`.
    assert.match(md, /!\[Architecture — generated by codeshot\]/);
    assert.doesNotMatch(md, new RegExp(`!\\[${path.basename(path.resolve(repoRoot))} architecture`), 'alt must not leak the checkout dir name');
    const imgName = md.match(/\]\((codeshot-arch-[^)]+\.svg)\)/)[1];
    assert.ok(fs.existsSync(path.join(dir, imgName)), 'expected the image written next to the doc');

    // 2) --check on the freshly-embedded doc: up to date → exit 0
    const okOut = execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--embed', doc, '--format', 'svg', '--check'], { encoding: 'utf8', stdio: 'pipe' });
    assert.match(okOut, /up to date/);

    // 3) drift: corrupt the committed image → --check must fail (exit 1)
    fs.writeFileSync(path.join(dir, imgName), '<svg>tampered</svg>', 'utf8');
    let threw = false;
    try {
      execFileSync('node', [callgraphJs, '--architecture', '--path', repoRoot, '--embed', doc, '--format', 'svg', '--check'], { encoding: 'utf8', stdio: 'pipe' });
    } catch (err) {
      threw = true;
      assert.match(err.stderr, /out of date/);
    }
    assert.strictEqual(threw, true, 'expected --check to fail on a stale image');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('CLI --embed into a nonexistent doc is rejected (refresh, not create)', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  let threw = false;
  try {
    execFileSync('node', [path.join(__dirname, '..', 'render', 'callgraph.js'), '--architecture', '--embed', path.join(require('os').tmpdir(), 'codeshot-nope-does-not-exist.md')], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /does not exist/);
  }
  assert.strictEqual(threw, true, 'expected --embed into a missing doc to be rejected');
});

test('CLI on an unindexed repo prints a clean "no index" message, not a raw codegraph error', () => {
  const { execFileSync } = require('child_process');
  const path = require('path');
  const fs = require('fs');
  const os = require('os');
  const callgraphJs = path.join(__dirname, '..', 'render', 'callgraph.js');

  // Needs codegraph on PATH to produce the real non-zero "not initialized" exit.
  try {
    execFileSync('codegraph', ['--version'], { stdio: 'pipe' });
  } catch {
    console.log('  # skipped: `codegraph` not on PATH');
    return;
  }

  // A fresh dir under tmp with no .codegraph anywhere above it → codegraph
  // reports "not initialized" rather than resolving a parent index.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-unindexed-'));
  fs.writeFileSync(path.join(dir, 'app.py'), 'def hello():\n    pass\n', 'utf8');
  let threw = false;
  try {
    execFileSync('node', [callgraphJs, 'hello', '--path', dir], { encoding: 'utf8', stdio: 'pipe' });
  } catch (err) {
    threw = true;
    assert.match(err.stderr, /has no index for/);
    assert.match(err.stderr, /codegraph init/);
    assert.doesNotMatch(err.stderr, /did not return JSON|Command failed/, 'must be the clean message, not the raw thrown error');
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
  assert.strictEqual(threw, true, 'expected exit 1 on an unindexed repo');
});

// --- svgStructure: version-independent --check comparison -------------

test('decodeXmlEntities undoes the entities graphviz emits in a <title>', () => {
  assert.strictEqual(decodeXmlEntities('a&#45;&gt;b'), 'a->b');
  assert.strictEqual(decodeXmlEntities('x &amp;&amp; y'), 'x && y');
  assert.strictEqual(decodeXmlEntities('&lt;tag&gt; &quot;q&quot; &apos;a&apos;'), '<tag> "q" \'a\'');
  assert.strictEqual(decodeXmlEntities('&#x41;&#x42;'), 'AB');
  // '&amp;' undone last so an already-encoded entity isn't double-decoded
  assert.strictEqual(decodeXmlEntities('&amp;lt;'), '&lt;');
});

// A minimal graphviz-shaped svg: the graph carries its own <title>, then each
// node/edge is a <g class="node|edge"> whose <title> is the id codeshot wrote.
const SVG_FIXTURE = `<?xml version="1.0"?>
<svg><g id="graph0" class="graph"><title>callgraph</title>
<g id="node1" class="node"><title>main</title><ellipse/></g>
<g id="node2" class="node"><title>buildDot</title><ellipse/></g>
<g id="edge1" class="edge"><title>main&#45;&gt;buildDot</title><path/></g>
</g></svg>`;

test('svgStructure extracts node/edge titles and skips the graph title', () => {
  assert.strictEqual(
    svgStructure(SVG_FIXTURE),
    'nodes:\nbuildDot\nmain\nedges:\nmain->buildDot'
  );
});

test('svgStructure is order-insensitive (reordered nodes → same signature)', () => {
  const reordered = SVG_FIXTURE
    .replace('<g id="node1" class="node"><title>main</title><ellipse/></g>\n', '')
    .replace('<g id="node2" class="node"><title>buildDot</title><ellipse/></g>',
      '<g id="node2" class="node"><title>buildDot</title><ellipse/></g>\n<g id="node1" class="node"><title>main</title><ellipse/></g>');
  assert.strictEqual(svgStructure(reordered), svgStructure(SVG_FIXTURE));
});

test('svgStructure tolerates class-before-id attribute order (graphviz version drift)', () => {
  const flipped = SVG_FIXTURE.replace(/<g id="([^"]*)" class="([^"]*)">/g, '<g class="$2" id="$1">');
  assert.strictEqual(svgStructure(flipped), svgStructure(SVG_FIXTURE));
});

test('svgStructure ignores cosmetic/version bytes but catches a dropped node or edge', () => {
  // version stamp + coordinate jitter must NOT change the signature
  const cosmetic = `<!-- Generated by graphviz version 2.42.2 -->\n` +
    SVG_FIXTURE.replace('<ellipse/>', '<ellipse cx="9" cy="9"/>');
  assert.strictEqual(svgStructure(cosmetic), svgStructure(SVG_FIXTURE));
  // but removing the edge is real drift
  const noEdge = SVG_FIXTURE.replace(/<g id="edge1"[\s\S]*?<\/g>\n/, '');
  assert.notStrictEqual(svgStructure(noEdge), svgStructure(SVG_FIXTURE));
});

test('svgStructure matches real graphviz output (buildDot rendered via dot -Tsvg)', () => {
  const { execFileSync } = require('child_process');
  const dot = buildDot('Target', [{ name: 'caller', filePath: 'a.js' }], [{ name: 'callee', filePath: 'b.js' }]);
  const svg = execFileSync('dot', ['-Tsvg'], { input: dot, encoding: 'utf8' });
  const sig = svgStructure(svg);
  // every node the DOT declared appears, and both directed edges are recovered
  for (const n of ['Target', 'caller', 'callee']) assert.match(sig, new RegExp(`(^|\\n)${n}(\\n|$)`));
  assert.match(sig, /caller->Target/);
  assert.match(sig, /Target->callee/);
});

// --- index health check (parseUnresolvedRefs) -------------------------

test('parseUnresolvedRefs extracts the count from codegraph status, comma- and ANSI-tolerant', () => {
  assert.strictEqual(parseUnresolvedRefs('[33m⚠[0m 4,303 references from an interrupted run are awaiting resolution — some callers/impact edges are missing. Run "codegraph sync" to resolve them.'), 4303);
  assert.strictEqual(parseUnresolvedRefs('186 references from an interrupted run'), 186);
  assert.strictEqual(parseUnresolvedRefs('1 reference from an interrupted run'), 1);
});

test('parseUnresolvedRefs returns null on a healthy status (no interrupted-run line)', () => {
  assert.strictEqual(parseUnresolvedRefs('Index Statistics:\n  Files: 40\n  Nodes: 900\n  Edges: 2000'), null);
  assert.strictEqual(parseUnresolvedRefs(''), null);
  assert.strictEqual(parseUnresolvedRefs('0 references from an interrupted run'), null, 'zero is healthy, not a warning');
});

// --- --diff mode --------------------------------------------------------

test('matchRootSymbols keeps only symbols whose filePath is a changed file', () => {
  const symbols = [
    { name: 'a', filePath: 'src/a.js' },
    { name: 'b', filePath: 'src/b.js' },
    { name: 'c', filePath: 'src/c.js' },
  ];
  const roots = matchRootSymbols(symbols, ['src/a.js', 'src/c.js']);
  assert.deepStrictEqual(roots.map(r => r.name), ['a', 'c']);
});

test('matchRootSymbols normalizes backslash separators on both sides', () => {
  const symbols = [{ name: 'a', filePath: 'src\\a.js' }];
  assert.strictEqual(matchRootSymbols(symbols, ['src/a.js']).length, 1);
});

test('matchRootSymbols ignores symbols with no filePath and returns [] on no match', () => {
  assert.deepStrictEqual(matchRootSymbols([{ name: 'a', filePath: null }], ['src/a.js']), []);
  assert.deepStrictEqual(matchRootSymbols([{ name: 'a', filePath: 'src/a.js' }], ['src/other.js']), []);
});

test('diffNoChangesWarning names the ref when given, and "matches HEAD" when not', () => {
  assert.match(diffNoChangesWarning(null), /working tree matches HEAD/);
  assert.match(diffNoChangesWarning('origin/main...HEAD'), /'origin\/main\.\.\.HEAD'/);
});

test('diffNoSymbolsWarning reports the changed-file count and a codegraph sync hint', () => {
  const msg = diffNoSymbolsWarning('/repo', 3);
  assert.match(msg, /3 changed file/);
  assert.match(msg, /codegraph sync \/repo/);
});

test('diffSymbolBudgetWarning is null under budget, fires and names the cut over budget', () => {
  assert.strictEqual(diffSymbolBudgetWarning(3, 5), null);
  assert.match(diffSymbolBudgetWarning(7, 5), /matched 7 changed symbols but only probing the first 5/);
});

test('diffEmbedRefusal names the embed target and the ref (or HEAD) it found nothing for', () => {
  assert.match(diffEmbedRefusal(null, 'docs.md'), /working tree matches HEAD/);
  assert.match(diffEmbedRefusal(null, 'docs.md'), /'docs\.md'/);
  assert.match(diffEmbedRefusal('origin/main...HEAD', 'docs.md'), /'origin\/main\.\.\.HEAD'/);
});

test('diffEmptyRootsWarning is null when every root has at least one edge, fires with the aggregate count otherwise', () => {
  assert.strictEqual(diffEmptyRootsWarning(0, 5), null);
  assert.match(diffEmptyRootsWarning(2, 5), /2 of 5 changed symbol\(s\) have no callers or callees/);
});

test('diffEmbedRefusalNoSymbols reports the changed-file count, the embed target, and a codegraph sync hint, but not the "found no changed files" wording diffEmbedRefusal uses', () => {
  const msg = diffEmbedRefusalNoSymbols('/repo', 3, 'docs.md');
  assert.match(msg, /3 changed file\(s\) but no matching symbols/);
  assert.match(msg, /refusing to overwrite the existing diagram embedded in 'docs\.md'/);
  assert.match(msg, /codegraph sync \/repo/);
  assert.doesNotMatch(msg, /found no changed files/);
});

test('diffDuplicateNameWarning fires on a real cross-file collision and never claims the node -f re-probe --architecture does', () => {
  const symbols = [
    { name: 'parse', filePath: 'a.js' },
    { name: 'parse', filePath: 'b.js' },
    { name: 'unique', filePath: 'c.js' },
  ];
  const msg = diffDuplicateNameWarning(symbols);
  assert.match(msg, /1 symbol name\(s\).*parse/);
  assert.doesNotMatch(msg, /re-probes/);
  assert.strictEqual(diffDuplicateNameWarning([{ name: 'unique', filePath: 'c.js' }]), null);
});

test('nodeKey pairs name and filePath so distinct symbols never collide by name alone', () => {
  assert.strictEqual(nodeKey({ name: 'foo', filePath: 'a.js' }), 'foo a.js');
  assert.notStrictEqual(
    nodeKey({ name: 'foo', filePath: 'a.js' }),
    nodeKey({ name: 'foo', filePath: 'b.js' }),
  );
});

test('diffTruncationWarning is null with no truncated roots, fires with the aggregate count and examples otherwise', () => {
  assert.strictEqual(diffTruncationWarning([], 50), null);
  const msg = diffTruncationWarning(['foo', 'bar'], 50);
  assert.match(msg, /2 changed symbol\(s\)/);
  assert.match(msg, /--limit \(50\)/);
  assert.match(msg, /foo, bar/);
});

test('diffNothingToCheck / diffNothingToCheckNoSymbols name the embed target and never claim "refusing to overwrite"', () => {
  const a = diffNothingToCheck(null, 'docs.md');
  assert.match(a, /nothing to diagram, so nothing to check/);
  assert.match(a, /'docs\.md'/);
  assert.doesNotMatch(a, /refusing to overwrite/);

  const b = diffNothingToCheckNoSymbols('/repo', 3, 'docs.md');
  assert.match(b, /3 changed file\(s\) but no matching symbols/);
  assert.match(b, /nothing to diagram, so nothing to check/);
  assert.doesNotMatch(b, /refusing to overwrite/);
});

test('diffHandleEmptyRoots logs only the warn message (never exits) when there is no embedFile', () => {
  const originalError = console.error;
  const logs = [];
  console.error = (msg) => logs.push(msg);
  try {
    diffHandleEmptyRoots(null, false, 'REFUSAL', 'WARN', 'NOTHING_TO_CHECK');
  } finally {
    console.error = originalError;
  }
  assert.deepStrictEqual(logs, ['WARN']);
  // The embedFile && !check → refusal (exit 1) and embedFile && check →
  // "nothing to check" (exit 0) branches are exercised via the real CLI
  // (see the --diff --embed [--check] CLI tests below) — calling
  // process.exit directly in-process here would kill the test runner itself.
});

test('matchRootSymbols excludes "kind":"file" index entries — a changed FILE itself must not become a diagram root', () => {
  const symbols = [
    { name: 'a.js', filePath: 'a.js', kind: 'file' },
    { name: 'realSymbol', filePath: 'a.js' },
  ];
  const roots = matchRootSymbols(symbols, ['a.js']);
  assert.deepStrictEqual(roots.map(r => r.name), ['realSymbol']);
});

test('buildDiffDot bolds every root and draws caller -> root / root -> callee edges', () => {
  const roots = [{ name: 'Root', filePath: 'src/root.js' }];
  const edges = [
    { from: { name: 'caller', filePath: 'a.js' }, to: roots[0] },
    { from: roots[0], to: { name: 'callee', filePath: 'b.js' } },
  ];
  const dot = buildDiffDot(roots, edges);
  assert.match(dot, /"Root" \[fillcolor="#e2e8f0".*fontname="Helvetica-Bold"/);
  assert.match(dot, /"caller" -> "Root"/);
  assert.match(dot, /"Root" -> "callee"/);
});

test('buildDiffDot dashes an edge whose actual SOURCE is test code, whether that source is a plain caller or a root calling out', () => {
  const roots = [{ name: 'Root', filePath: 'test/root.spec.js' }];
  const edges = [
    { from: { name: 'callerTest', filePath: 'test/caller.spec.js' }, to: roots[0] },
    { from: roots[0], to: { name: 'callee', filePath: 'b.js' } },
  ];
  const dot = buildDiffDot(roots, edges);
  assert.match(dot, /"callerTest" -> "Root" \[style=dashed, label="test"\];/);
  // Root itself lives in test/root.spec.js, so a call it makes IS a test
  // calling production code — unlike buildDot's single-root mode, this is
  // deliberately not suppressed here (see buildDiffDot's edge-styling comment).
  assert.match(dot, /"Root" -> "callee" \[style=dashed, label="test"\];/);
});

test('buildDiffDot dotted-styles a file-kind endpoint on either side, and gives a file-kind source priority over a test-kind target', () => {
  const roots = [{ name: 'Root', filePath: 'src/root.js' }];
  const edges = [
    { from: { name: 'root.js', filePath: 'src/root.js', kind: 'file' }, to: roots[0] },
    { from: roots[0], to: { name: 'unresolved.js', filePath: 'lib/unresolved.js', kind: 'file' } },
  ];
  const dot = buildDiffDot(roots, edges);
  assert.match(dot, /"root\.js" -> "Root" \[style=dotted, color="#9ca3af", label="file"\];/);
  assert.match(dot, /"Root" -> "unresolved\.js" \[style=dotted, color="#9ca3af", label="file"\];/);
});

test('buildDiffDot always draws every root even when --max-render caps non-root nodes to fewer than the roots', () => {
  const roots = [{ name: 'R1', filePath: 'a.js' }, { name: 'R2', filePath: 'b.js' }];
  const edges = [
    { from: { name: 'c1', filePath: 'c1.js' }, to: roots[0] },
    { from: { name: 'c2', filePath: 'c2.js' }, to: roots[1] },
  ];
  const dot = buildDiffDot(roots, edges, { maxRender: 1 });
  assert.match(dot, /"R1"/);
  assert.match(dot, /"R2"/);
  // exactly one of the two callers survives the maxRender:1 cut, not both
  const keptCallers = ['c1', 'c2'].filter(n => dot.includes(`"${n}"`));
  assert.strictEqual(keptCallers.length, 1);
});

test('buildDiffDot dedupes a root-to-root edge discovered via both probes (one root\'s caller-probe, the other\'s callee-probe), regardless of which is pushed first', () => {
  const r1 = { name: 'R1', filePath: 'a.js' };
  const r2 = { name: 'R2', filePath: 'b.js' };
  // {from: r1, to: r2} is the exact same real call whether it's found as
  // r2's caller (r1 calls r2) or r1's callee (r1 calls r2) — dedupeEdges'
  // from/to-only key collapses them into one edge no matter which the
  // caller-probe/callee-probe loop in runDiffMode pushed first.
  const asCallerFirst = [{ from: r1, to: r2 }, { from: r1, to: r2 }];
  const asCalleeFirst = [{ from: r1, to: r2 }, { from: r1, to: r2 }];
  for (const edges of [asCallerFirst, asCalleeFirst]) {
    const dot = buildDiffDot([r1, r2], edges);
    assert.strictEqual((dot.match(/"R1" -> "R2"/g) || []).length, 1);
  }
});

test('buildDiffDot styles a root-to-root edge the same way regardless of push order (order-independence, not just value-dedup)', () => {
  // R1 lives in a test file — a call it makes should read as dashed "test"
  // however the edge was assembled, not just when the object instances differ.
  const r1 = { name: 'R1', filePath: 'test/r1.spec.js' };
  const r2 = { name: 'R2', filePath: 'b.js' };
  const dotA = buildDiffDot([r1, r2], [{ from: r1, to: r2 }]);
  const dotB = buildDiffDot([r2, r1], [{ from: r1, to: r2 }]); // roots array order flipped
  for (const dot of [dotA, dotB]) {
    assert.match(dot, /"R1" -> "R2" \[style=dashed, label="test"\];/);
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
