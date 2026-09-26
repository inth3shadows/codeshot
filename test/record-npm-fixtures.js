#!/usr/bin/env node
'use strict';

// Re-records the npm-codegraph output that test/run.js replays to cover
// --architecture's `node -f` fallback (probeSymbolCallees). Only CI's npm-codegraph
// jobs run that route live; the plain Node test jobs have no codegraph and rely
// on these files, and this script is how they stay true to what npm codegraph
// actually prints instead of drifting into hand-edited strings.
//
// Usage: node test/record-npm-fixtures.js [--codegraph <bin>]
//   --codegraph defaults to `codegraph` on PATH; pass the path of an npm
//   install (`npm install -g @colbymchenry/codegraph@<version>`) when the one on
//   PATH is a fork build.
//
// Refuses to record from a build that returns per-definition JSON (upstream
// #1801): that route never reaches `node -f`, so its output would silently turn
// the fallback tests into tests of the other route.

const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const argIdx = process.argv.indexOf('--codegraph');
const bin = argIdx !== -1 ? process.argv[argIdx + 1] : 'codegraph';
if (!bin) {
  console.error('record-npm-fixtures: --codegraph needs a value');
  process.exit(2);
}
const outDir = path.join(__dirname, 'fixtures', 'npm-codegraph');
const run = args => execFileSync(bin, args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 180000 });

// The same two-`handle` shape as test/run.js's --architecture CLI fixture:
// a/svc.js calls alpha, b/svc.js calls beta, so a bare-name probe is ambiguous.
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'codeshot-record-'));
try {
  fs.mkdirSync(path.join(dir, 'a'));
  fs.mkdirSync(path.join(dir, 'b'));
  fs.writeFileSync(path.join(dir, 'a', 'svc.js'), 'const { alpha } = require("./alpha");\nfunction handle() { return alpha(); }\nmodule.exports = { handle };\n');
  fs.writeFileSync(path.join(dir, 'a', 'alpha.js'), 'function alpha() { return 1; }\nmodule.exports = { alpha };\n');
  fs.writeFileSync(path.join(dir, 'b', 'svc.js'), 'const { beta } = require("../b/beta");\nfunction handle() { return beta(); }\nmodule.exports = { handle };\n');
  fs.writeFileSync(path.join(dir, 'b', 'beta.js'), 'function beta() { return 2; }\nmodule.exports = { beta };\n');

  const version = run(['--version']).trim();
  run(['init', dir]);
  const callees = JSON.parse(run(['callees', '--path', dir, '--json', '--', 'handle']));
  if (Array.isArray(callees.definitions)) {
    console.error(`record-npm-fixtures: ${bin} (${version}) returns per-definition JSON — that is not npm codegraph's fallback shape. Use an npm release through 1.6.0.`);
    process.exit(1);
  }
  const nodeA = run(['node', '--path', dir, '-f', 'a/svc.js', '--', 'handle']);
  if (!nodeA.includes('**Calls →**')) {
    console.error(`record-npm-fixtures: 'node -f' output from ${version} has no '**Calls →**' trail line — the format parseNodeCalls reads may have changed; not overwriting fixtures.`);
    process.exit(1);
  }

  fs.mkdirSync(outDir, { recursive: true });
  fs.writeFileSync(path.join(outDir, 'handle-callees.json'), `${JSON.stringify(callees, null, 2)}\n`);
  fs.writeFileSync(path.join(outDir, 'handle-node-a.txt'), nodeA);
  fs.writeFileSync(path.join(outDir, 'VERSION'), `${version}\n`);
  console.log(`record-npm-fixtures: recorded codegraph ${version} output into ${path.relative(process.cwd(), outDir)}/`);
} finally {
  fs.rmSync(dir, { recursive: true, force: true });
}
