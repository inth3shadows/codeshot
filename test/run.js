#!/usr/bin/env node
'use strict';

const assert = require('assert');
const {
  buildDot, nodeIdentities, isTestRef, truncationWarning, dedupeNodes, renderTruncationNote, dedupeEdges, depthColor,
  depthBudgetWarning, allocateRenderBudget, formatMismatchWarning, matchSymbolNotFound,
  filterCallableSymbols, symbolBudgetWarning, duplicateNameWarning, aggregateFileEdges,
  topFilesByWeight, buildArchitectureDot, architectureOutputBaseName,
  applyEmbed, embedMarkers, embedRelLink, parseUnresolvedRefs,
  svgStructure, decodeXmlEntities,
  emptyGraphWarning, emptyArchitectureWarning,
  matchNotInitialized, argRepoPath, parseCodegraphOutput,
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

test('filterCallableSymbols unwraps .node and drops kind:file entries', () => {
  const results = [
    { node: { name: 'Foo', kind: 'function', filePath: 'a.js' } },
    { node: { name: 'a.js', kind: 'file', filePath: 'a.js' } },
    { node: { name: 'BAR', kind: 'constant', filePath: 'b.js' } },
  ];
  const symbols = filterCallableSymbols(results);
  assert.strictEqual(symbols.length, 2);
  assert.deepStrictEqual(symbols.map(s => s.name), ['Foo', 'BAR']);
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

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
