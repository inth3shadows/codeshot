#!/usr/bin/env node
'use strict';

const assert = require('assert');
const { buildDot, isTestRef, truncationWarning, dedupeNodes, renderTruncationNote } = require('../render/callgraph.js');

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
  assert.match(dot, /"RollAutoSnapshot" \[fillcolor="#c7d2fe"/);
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

test('buildDot keeps distinct callers/callees with the same name but different filePath', () => {
  const dot = buildDot('Target', [{ name: 'Caller', filePath: 'a.js' }, { name: 'Caller', filePath: 'b.js' }], []);
  assert.strictEqual((dot.match(/"Caller" -> "Target"/g) || []).length, 2);
});

test('buildDot renders every caller/callee when maxRender is omitted', () => {
  const many = Array.from({ length: 5 }, (_, i) => ({ name: `Fn${i}`, filePath: `src/fn${i}.js` }));
  const dot = buildDot('Target', many, many);
  assert.strictEqual((dot.match(/-> "Target"/g) || []).length, 5);
  assert.strictEqual((dot.match(/"Target" ->/g) || []).length, 5);
});

test('buildDot caps rendered callers/callees at maxRender, keeping the first N distinct entries', () => {
  const many = Array.from({ length: 5 }, (_, i) => ({ name: `Fn${i}`, filePath: `src/fn${i}.js` }));
  const dot = buildDot('Target', many, many, { maxRender: 2 });
  assert.strictEqual((dot.match(/-> "Target"/g) || []).length, 2);
  assert.strictEqual((dot.match(/"Target" ->/g) || []).length, 2);
  assert.match(dot, /"Fn0" -> "Target"/);
  assert.match(dot, /"Fn1" -> "Target"/);
  assert.doesNotMatch(dot, /"Fn2" -> "Target"/);
});

test('buildDot maxRender applies after dedup, not before', () => {
  const dupe = { name: 'Caller', filePath: 'src/caller.js' };
  const dot = buildDot('Target', [dupe, { ...dupe }, { name: 'Other', filePath: 'src/other.js' }], [], { maxRender: 2 });
  assert.match(dot, /"Caller" -> "Target"/);
  assert.match(dot, /"Other" -> "Target"/);
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

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
