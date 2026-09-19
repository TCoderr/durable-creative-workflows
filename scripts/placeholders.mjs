#!/usr/bin/env node
/**
 * scripts/placeholders.mjs — authored stand-ins for the two hero portraits.
 *
 * Writes public/portrait_top.png and public/portrait_bottom.png at 4:5 when
 * those files are absent (pass --force to overwrite, --out DIR to write
 * elsewhere). Both are drawn from the same geometry so the reveal reads as one
 * figure changing state: the top is warm paper with a matte figure and faint
 * contour lines; the bottom is ink with the same figure in a glossy, synthetic
 * finish and a single light seam. No fonts, no dependencies, and the output is
 * deterministic byte for byte.
 *
 *   node scripts/placeholders.mjs            # writes only missing files
 *   node scripts/placeholders.mjs --force    # overwrites existing files
 *   node scripts/placeholders.mjs --out DIR  # writes into DIR
 */

import { deflateSync } from 'node:zlib';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const WIDTH = 1200;
const HEIGHT = 1500; // 4:5

const args = process.argv.slice(2);
const force = args.includes('--force');
const outIndex = args.indexOf('--out');
const outDir = resolve(outIndex >= 0 ? args[outIndex + 1] : 'public');

/* ------------------------------------------------------------------ *
 * Small math helpers
 * ------------------------------------------------------------------ */

const clamp01 = (x) => (x < 0 ? 0 : x > 1 ? 1 : x);
const mix = (a, b, t) => a + (b - a) * t;
const smoothstep = (e0, e1, x) => {
  const t = clamp01((x - e0) / (e1 - e0));
  return t * t * (3 - 2 * t);
};
const hex = (h) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
const mix3 = (a, b, t) => [
  mix(a[0], b[0], t),
  mix(a[1], b[1], t),
  mix(a[2], b[2], t),
];

// Deterministic per-pixel grain in [-0.5, 0.5]. Same input, same file.
function grain(x, y) {
  const s = Math.sin(x * 12.9898 + y * 78.233) * 43758.5453;
  return s - Math.floor(s) - 0.5;
}

/* ------------------------------------------------------------------ *
 * Shared geometry — identical in both files
 * ------------------------------------------------------------------ */

// Signed distance to an axis-aligned ellipse, in pixels (approximate).
function ellipse(px, py, cx, cy, rx, ry) {
  const dx = (px - cx) / rx;
  const dy = (py - cy) / ry;
  return (Math.hypot(dx, dy) - 1) * Math.min(rx, ry);
}

// Signed distance to a rounded box.
function box(px, py, cx, cy, hw, hh, r) {
  const qx = Math.abs(px - cx) - hw + r;
  const qy = Math.abs(py - cy) - hh + r;
  const outside = Math.hypot(Math.max(qx, 0), Math.max(qy, 0));
  return outside + Math.min(Math.max(qx, qy), 0) - r;
}

// Head, neck and shoulders. Feathered later so edges stay soft.
function figure(px, py) {
  const head = ellipse(
    px,
    py,
    WIDTH * 0.5,
    HEIGHT * 0.4,
    WIDTH * 0.19,
    HEIGHT * 0.235,
  );
  const neck = box(
    px,
    py,
    WIDTH * 0.5,
    HEIGHT * 0.655,
    WIDTH * 0.065,
    HEIGHT * 0.06,
    24,
  );
  const shoulders = ellipse(
    px,
    py,
    WIDTH * 0.5,
    HEIGHT * 1.1,
    WIDTH * 0.5,
    HEIGHT * 0.43,
  );
  return Math.min(head, neck, shoulders);
}

// Faint contour lines in the site's grammar: sine-offset horizontal bands.
function contour(u, v) {
  const n = 16;
  const c =
    v * n +
    0.55 * Math.sin(u * 6.2832 * 1.35 + v * 3.1) +
    0.3 * Math.sin(u * 6.2832 * 2.7 - v * 5.3) +
    0.18 * Math.sin(u * 6.2832 * 4.1 + 1.7);
  const d = Math.abs(c - Math.round(c));
  const w = (n / HEIGHT) * 1.1; // about one device pixel
  return 1 - smoothstep(w * 0.4, w * 2.2, d);
}

/* ------------------------------------------------------------------ *
 * The two treatments
 * ------------------------------------------------------------------ */

const PAPER = hex('#ece9e1');
const PAPER_LOW = hex('#e3dfd4');
const INK_LINE = hex('#161513');
const MATTE_LIGHT = hex('#d9cdbc');
const MATTE_SHADE = hex('#b5a58f');

const INK = hex('#0a0a0a');
const INK_HIGH = hex('#161616');
const PAPER_LINE = hex('#f4f1ea');
const GLOSS_BASE = hex('#111111');
const GLOSS_SPEC = hex('#4a4a4a');
const GLOSS_RIM = hex('#2a2a2a');
const SEAM = hex('#f2dfbd');

function top(x, y) {
  const u = x / WIDTH;
  const v = y / HEIGHT;

  // Paper with a gentle fall-off toward the foot and the corners.
  const vignette = 1 - 0.06 * Math.pow(Math.hypot(u - 0.5, v - 0.5) * 1.4, 2);
  let rgb = mix3(PAPER, PAPER_LOW, v * 0.5).map((c) => c * vignette);

  // Contour lines at 10% ink.
  rgb = mix3(rgb, INK_LINE, contour(u, v) * 0.1);

  // Matte figure lit from the upper left.
  const d = figure(x, y);
  const mask = 1 - smoothstep(-8, 8, d);
  const light = clamp01(0.25 + (u - 0.3) * 0.9 + (v - 0.35) * 0.55);
  const tone = mix3(MATTE_LIGHT, MATTE_SHADE, light);
  rgb = mix3(rgb, tone, mask);

  // Matte grain, stronger inside the figure.
  const g = grain(x, y) * (2 + 3 * mask);
  return rgb.map((c) => c + g);
}

function bottom(x, y) {
  const u = x / WIDTH;
  const v = y / HEIGHT;

  let rgb = mix3(INK_HIGH, INK, v);

  // Same contour lines, now light on dark.
  rgb = mix3(rgb, PAPER_LINE, contour(u, v) * 0.14);

  // Same figure, glossy: a broad specular lobe and a rim on the far edge.
  const d = figure(x, y);
  const mask = 1 - smoothstep(-8, 8, d);
  const spec =
    1 - smoothstep(0, 1, Math.hypot((u - 0.42) / 0.14, (v - 0.31) / 0.12));
  const rim =
    (1 - smoothstep(-22, -2, d)) *
    smoothstep(-40, -2, d) *
    smoothstep(0.45, 0.7, u);
  let tone = mix3(GLOSS_BASE, GLOSS_SPEC, spec * spec);
  tone = mix3(tone, GLOSS_RIM, rim * 0.9);
  rgb = mix3(rgb, tone, mask);

  // One light seam across the visor line, inside the head only.
  const seamV = 0.43 + 0.035 * Math.sin((u - 0.5) * 5.2);
  const seam =
    (1 - smoothstep(0.0012, 0.0032, Math.abs(v - seamV))) *
    (1 - smoothstep(-40, -20, d)) *
    smoothstep(0.33, 0.4, u) *
    (1 - smoothstep(0.62, 0.68, u));
  rgb = mix3(rgb, SEAM, seam);

  const g = grain(x, y) * 1.5;
  return rgb.map((c) => c + g);
}

/* ------------------------------------------------------------------ *
 * Minimal PNG writer (8-bit RGB, filter 0, one IDAT)
 * ------------------------------------------------------------------ */

const CRC_TABLE = new Uint32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buf) {
  let c = 0xffffffff;
  for (let i = 0; i < buf.length; i++) {
    c = CRC_TABLE[(c ^ buf[i]) & 0xff] ^ (c >>> 8);
  }
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const t = Buffer.from(type, 'ascii');
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([t, data])));
  return Buffer.concat([len, t, data, crc]);
}

function encodePng(width, height, rgb, texts) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // colour type: RGB
  const text = Object.entries(texts).map(([k, v]) =>
    chunk('tEXt', Buffer.from(`${k}\0${v}`, 'latin1')),
  );
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    ...text,
    chunk('IDAT', deflateSync(rgb, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

function render(shade) {
  const stride = WIDTH * 3 + 1;
  const raw = Buffer.alloc(stride * HEIGHT);
  for (let y = 0; y < HEIGHT; y++) {
    const row = y * stride;
    raw[row] = 0; // filter: none
    for (let x = 0; x < WIDTH; x++) {
      const [r, g, b] = shade(x, y);
      const o = row + 1 + x * 3;
      raw[o] = Math.max(0, Math.min(255, Math.round(r)));
      raw[o + 1] = Math.max(0, Math.min(255, Math.round(g)));
      raw[o + 2] = Math.max(0, Math.min(255, Math.round(b)));
    }
  }
  return raw;
}

/* ------------------------------------------------------------------ *
 * Main
 * ------------------------------------------------------------------ */

mkdirSync(outDir, { recursive: true });

const files = [
  [
    'portrait_top.png',
    top,
    'VELIN placeholder, warm paper state. Replace with an authored 4:5 portrait.',
  ],
  [
    'portrait_bottom.png',
    bottom,
    'VELIN placeholder, ink state, same figure. Replace with an authored 4:5 portrait.',
  ],
];

for (const [name, shade, comment] of files) {
  const path = join(outDir, name);
  if (existsSync(path) && !force) {
    console.log(`kept    ${path} (exists; pass --force to overwrite)`);
    continue;
  }
  const png = encodePng(WIDTH, HEIGHT, render(shade), {
    Comment: comment,
    Software: 'scripts/placeholders.mjs',
  });
  writeFileSync(path, png);
  console.log(`wrote   ${path} ${WIDTH}x${HEIGHT} ${png.length} bytes`);
}
