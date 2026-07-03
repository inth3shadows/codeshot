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

const { execFileSync, execFile } = require('child_process');
const { parseArgs, promisify } = require('util');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

const execFileAsync = promisify(execFile);

const DEFAULT_LIMIT = 50;

function requireOnPath(bin, installHint) {
  try {
    execFileSync(process.platform === 'win32' ? 'where' : 'which', [bin], { stdio: 'ignore' });
  } catch {
    console.error(`codeshot: '${bin}' not found on PATH. ${installHint}`);
    process.exit(1);
  }
}

function parseCodegraphOutput(out, args) {
  try {
    return JSON.parse(out);
  } catch {
    console.error(`codeshot: 'codegraph ${args.join(' ')}' did not return JSON:\n${out.trim()}`);
    process.exit(1);
  }
}

async function runCodegraph(args) {
  const { stdout } = await execFileAsync('codegraph', args, { encoding: 'utf8' });
  return parseCodegraphOutput(stdout, args);
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

const USAGE = 'Usage: callgraph.js <symbol> [--path <repoPath>] [--out <file.png>] [--limit <n>]';

async function main() {
  let values, positionals;
  try {
    ({ values, positionals } = parseArgs({
      args: process.argv.slice(2),
      options: {
        path:  { type: 'string', default: '.' },
        out:   { type: 'string' },
        limit: { type: 'string', default: String(DEFAULT_LIMIT) },
      },
      allowPositionals: true,
    }));
  } catch (err) {
    if (err.code === 'ERR_PARSE_ARGS_INVALID_OPTION_VALUE' && /--limit/.test(err.message)) {
      const flagIndex = process.argv.indexOf('--limit');
      const badValue = flagIndex !== -1 ? process.argv[flagIndex + 1] : undefined;
      console.error(`codeshot: --limit must be a positive integer, got '${badValue}'`);
    } else {
      console.error(`codeshot: ${err.message}`);
    }
    console.error(USAGE);
    process.exit(1);
  }

  const symbol = positionals[0];
  if (!symbol) {
    console.error('codeshot: missing required <symbol> argument');
    console.error(USAGE);
    process.exit(1);
  }

  const repoPath = values.path;
  let   outFile  = values.out || null;
  const limit    = Number(values.limit);
  if (!Number.isInteger(limit) || limit <= 0) {
    console.error(`codeshot: --limit must be a positive integer, got '${values.limit}'`);
    process.exit(1);
  }

  const safeSymbol = sanitizeForFilename(symbol);
  if (!outFile) {
    outFile = path.join(os.tmpdir(), `callgraph-${safeSymbol}-${Date.now()}.png`);
  }

  requireOnPath('codegraph', 'Install: https://github.com/colbymchenry/codegraph');
  requireOnPath('dot', 'Install graphviz (e.g. `brew install graphviz` or `apt install graphviz`).');

  // Sequential, not Promise.all: concurrent codegraph invocations against the
  // same SQLite index intermittently race on codegraph's own schema_versions
  // table ("UNIQUE constraint failed"), confirmed by running these calls in
  // parallel — codegraph is not safe to invoke concurrently against one index.
  const { callers } = await runCodegraph(['callers', symbol, '--path', repoPath, '--limit', String(limit), '--json']);
  const { callees } = await runCodegraph(['callees', symbol, '--path', repoPath, '--limit', String(limit), '--json']);

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
  main().catch(err => {
    console.error(`codeshot: ${err.message}`);
    process.exit(1);
  });
}

module.exports = { buildDot, isTestRef, truncationWarning };
