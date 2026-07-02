#!/usr/bin/env node
'use strict';

/**
 * Render a symbol's caller/callee trail as a call-graph image.
 *
 * Pulls structured data from the CodeGraph CLI (`codegraph callers`/`callees
 * --json`) and renders it through graphviz (`dot`). Requires both on PATH.
 *
 * Usage:
 *   node render/callgraph.js <symbol> [--path <repoPath>] [--out <file.png>]
 */

const { execFileSync } = require('child_process');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

function requireOnPath(bin, installHint) {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin], { stdio: 'ignore' });
  } catch {
    console.error(`codeshot: '${bin}' not found on PATH. ${installHint}`);
    process.exit(1);
  }
}

function runCodegraph(args) {
  const out = execFileSync('codegraph', args, { encoding: 'utf8' });
  try {
    return JSON.parse(out);
  } catch {
    console.error(`codeshot: 'codegraph ${args.join(' ')}' did not return JSON:\n${out.trim()}`);
    process.exit(1);
  }
}

function sanitizeForFilename(s) {
  return String(s).replace(/[\\/:*?"<>|]/g, '_');
}

function isTestRef(node) {
  return /test/i.test(node.name) || /test/i.test(node.filePath);
}

function buildDot(symbol, callers = [], callees = []) {
  const esc = s => String(s).replace(/"/g, '\\"');
  const lines = [
    'digraph callgraph {',
    '  rankdir=LR;',
    '  node [shape=box, style="rounded,filled", fillcolor="#eef2ff", fontname="Helvetica", fontsize=11];',
    '  edge [color="#6366f1", arrowsize=0.7];',
    `  "${esc(symbol)}" [fillcolor="#c7d2fe", fontname="Helvetica-Bold"];`,
  ];

  for (const c of callers) {
    const style = isTestRef(c) ? ' [style=dashed, label="test"]' : '';
    lines.push(`  "${esc(c.name)}" -> "${esc(symbol)}"${style};`);
  }
  for (const c of callees) {
    lines.push(`  "${esc(symbol)}" -> "${esc(c.name)}";`);
  }

  lines.push('}');
  return lines.join('\n');
}

function main() {
  const [, , symbol, ...rest] = process.argv;
  if (!symbol) {
    console.error('Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>]');
    process.exit(1);
  }

  let repoPath = '.';
  let outFile  = null;
  for (let i = 0; i < rest.length; i++) {
    const isPath = rest[i] === '--path';
    const isOut  = rest[i] === '--out';
    if (!isPath && !isOut) continue;

    const value = rest[i + 1];
    if (value === undefined || value === '--path' || value === '--out') {
      console.error(`codeshot: missing value for ${rest[i]}`);
      console.error('Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>]');
      process.exit(1);
    }
    if (isPath) repoPath = value; else outFile = value;
    i++;
  }
  const safeSymbol = sanitizeForFilename(symbol);
  if (!outFile) {
    outFile = path.join(os.tmpdir(), `callgraph-${safeSymbol}-${Date.now()}.png`);
  }

  requireOnPath('codegraph', 'Install: https://github.com/colbymchenry/codegraph');
  requireOnPath('dot', 'Install graphviz (e.g. `brew install graphviz` or `apt install graphviz`).');

  const { callers } = runCodegraph(['callers', symbol, '--path', repoPath, '--json']);
  const { callees } = runCodegraph(['callees', symbol, '--path', repoPath, '--json']);

  const dot = buildDot(symbol, callers || [], callees || []);
  const dotFile = path.join(os.tmpdir(), `callgraph-${safeSymbol}-${Date.now()}.dot`);
  fs.writeFileSync(dotFile, dot, 'utf8');

  execFileSync('dot', ['-Tpng', dotFile, '-o', outFile]);
  fs.unlinkSync(dotFile);

  console.log(outFile);
}

if (require.main === module) {
  main();
}

module.exports = { buildDot, isTestRef };
