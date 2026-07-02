#!/usr/bin/env node
'use strict';

/**
 * Render a symbol's caller/callee trail as a call-graph image.
 *
 * Pulls structured data from the CodeGraph CLI (`codegraph callers`/`callees
 * --json`) and renders it through graphviz (`dot`). Requires both on PATH.
 *
 * Usage:
 *   node render/callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>]
 */

const { execFileSync } = require('child_process');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

const DEFAULT_LIMIT = 50;

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

const FLAGS = ['--path', '--out', '--limit'];

function main() {
  const [, , symbol, ...rest] = process.argv;
  if (!symbol) {
    console.error('Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>]');
    process.exit(1);
  }

  let repoPath = '.';
  let outFile  = null;
  let limit    = DEFAULT_LIMIT;
  for (let i = 0; i < rest.length; i++) {
    if (!FLAGS.includes(rest[i])) continue;

    const value = rest[i + 1];
    if (value === undefined || FLAGS.includes(value)) {
      console.error(`codeshot: missing value for ${rest[i]}`);
      console.error('Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>]');
      process.exit(1);
    }
    if (rest[i] === '--path') repoPath = value;
    else if (rest[i] === '--out') outFile = value;
    else {
      limit = Number(value);
      if (!Number.isInteger(limit) || limit <= 0) {
        console.error(`codeshot: --limit must be a positive integer, got '${value}'`);
        process.exit(1);
      }
    }
    i++;
  }
  const safeSymbol = sanitizeForFilename(symbol);
  if (!outFile) {
    outFile = path.join(os.tmpdir(), `callgraph-${safeSymbol}-${Date.now()}.png`);
  }

  requireOnPath('codegraph', 'Install: https://github.com/colbymchenry/codegraph');
  requireOnPath('dot', 'Install graphviz (e.g. `brew install graphviz` or `apt install graphviz`).');

  const { callers } = runCodegraph(['callers', symbol, '--path', repoPath, '--limit', String(limit), '--json']);
  const { callees } = runCodegraph(['callees', symbol, '--path', repoPath, '--limit', String(limit), '--json']);

  for (const [kind, results] of [['callers', callers], ['callees', callees]]) {
    const warning = truncationWarning(kind, results, limit);
    if (warning) console.error(warning);
  }

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

module.exports = { buildDot, isTestRef, truncationWarning };
