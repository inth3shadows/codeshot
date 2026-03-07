#!/usr/bin/env node
'use strict';

/**
 * Codeshot Stop hook
 *
 * Fires after each Claude Code response via the Stop hook lifecycle event.
 * Reads the session JSONL, finds code blocks in the last assistant message,
 * and emits OSC 8 hyperlinks into the terminal so they're clickable.
 *
 * Clicking a link triggers the codeshot:// protocol handler which opens a
 * new Windows Terminal (or iTerm2) tab with the code pre-staged for review.
 *
 * Hook payload received on stdin (Claude Code Stop event):
 *   { session_id, transcript_path, cwd, stop_hook_active }
 */

const fs   = require('fs');
const os   = require('os');
const path = require('path');

// ── OSC 8 hyperlink helpers ────────────────────────────────────────────────

/**
 * Wrap text in an OSC 8 hyperlink.
 * Works in: Windows Terminal, iTerm2, Ghostty, WezTerm, Alacritty, Kitty.
 * Graceful degradation: terminals that don't support OSC 8 display plain text.
 */
function osc8(uri, text) {
  return `\x1b]8;;${uri}\x1b\\${text}\x1b]8;;\x1b\\`;
}

// ── Code block extraction ──────────────────────────────────────────────────

/** Extract all fenced code blocks from a markdown string. */
function extractCodeBlocks(text) {
  const blocks = [];
  // Match ```lang\ncode\n``` — handles optional trailing whitespace
  const re = /^```(\w*)\r?\n([\s\S]*?)^```[ \t]*$/gm;
  let m;
  while ((m = re.exec(text)) !== null) {
    const lang = m[1].toLowerCase() || '';
    const code = m[2].replace(/\r\n/g, '\n').replace(/\n$/, '');
    if (code.trim()) blocks.push({ lang, code });
  }
  return blocks;
}

// ── JSONL parsing ──────────────────────────────────────────────────────────

/** Find the last assistant text message in a Claude Code JSONL session file. */
function lastAssistantText(transcriptPath) {
  if (!fs.existsSync(transcriptPath)) return null;

  const raw = fs.readFileSync(transcriptPath, 'utf8');
  const lines = raw.split('\n').filter(Boolean);

  for (let i = lines.length - 1; i >= 0; i--) {
    let entry;
    try { entry = JSON.parse(lines[i]); } catch { continue; }

    // Claude Code JSONL entry shape:
    //   { type: 'assistant', message: { role: 'assistant', content: [...] } }
    if (entry.type !== 'assistant') continue;

    const content = entry.message?.content;
    if (!Array.isArray(content)) continue;

    const text = content
      .filter(b => b.type === 'text')
      .map(b => b.text)
      .join('\n');

    if (text.trim()) return text;
  }

  return null;
}

// ── URL building ───────────────────────────────────────────────────────────

const INLINE_LIMIT = 800; // chars — beyond this, write to temp file

function buildUri(code, cwd) {
  const params = new URLSearchParams({ cwd });
  if (code.length > INLINE_LIMIT) {
    const tmp = path.join(os.tmpdir(), `codeshot-${Date.now()}-${Math.random().toString(36).slice(2)}.ps1`);
    fs.writeFileSync(tmp, code, 'utf8');
    params.set('path', tmp);
    return `codeshot://file?${params}`;
  }
  params.set('code', code);
  return `codeshot://run?${params}`;
}

// ── RunEcho integration ────────────────────────────────────────────────────

/**
 * If RunEcho's faults.jsonl is present in the project, emit a CODE_STAGED
 * signal so M11 provenance can record that a human staged code for manual run.
 */
function maybeEmitRunEchoSignal(cwd, count) {
  try {
    const faultsLog = path.join(cwd, '.ai', 'faults.jsonl');
    if (!fs.existsSync(faultsLog)) return;
    const record = JSON.stringify({
      session_id: 'codeshot',
      signal:     'CODE_STAGED',
      ts:         new Date().toISOString(),
      value:      count,
      context:    `${count} code block(s) staged for manual execution`
    });
    fs.appendFileSync(faultsLog, record + '\n', 'utf8');
  } catch {
    // Never crash the hook over an optional integration
  }
}

// ── Output ─────────────────────────────────────────────────────────────────

function renderLinks(blocks, cwd) {
  const divider = '\x1b[2m' + '─'.repeat(52) + '\x1b[0m';
  const parts   = ['\n' + divider];

  for (const { lang, code } of blocks) {
    const uri     = buildUri(code, cwd);
    const label   = osc8(uri, '\x1b[36m▶ Run in terminal\x1b[0m');
    const langTag = lang ? ` \x1b[2m·\x1b[0m \x1b[33m${lang}\x1b[0m` : '';
    const preview = code.split('\n')[0].slice(0, 60) + (code.includes('\n') ? '\x1b[2m…\x1b[0m' : '');
    parts.push(`${label}${langTag}  ${preview}`);
  }

  parts.push(divider);
  return parts.join('\n') + '\n';
}

// ── Main ───────────────────────────────────────────────────────────────────

async function readStdin() {
  return new Promise(resolve => {
    let buf = '';
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', d => { buf += d; });
    process.stdin.on('end', () => resolve(buf));
    // Bail if stdin stays empty (e.g. manual invocation without payload)
    setTimeout(() => resolve(buf), 1000);
  });
}

async function main() {
  let payload = {};
  try { payload = JSON.parse(await readStdin()); } catch { /* no payload */ }

  const transcriptPath = payload.transcript_path;
  if (!transcriptPath) process.exit(0);

  const cwd  = payload.cwd || process.cwd();
  const text = lastAssistantText(transcriptPath);
  if (!text) process.exit(0);

  const blocks = extractCodeBlocks(text);
  if (blocks.length === 0) process.exit(0);

  process.stdout.write(renderLinks(blocks, cwd));
  maybeEmitRunEchoSignal(cwd, blocks.length);
}

main().catch(() => process.exit(0));
