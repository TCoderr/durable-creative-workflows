import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

const RECORD = JSON.parse(
  readFileSync(
    new URL('./fixtures/public-record.json', import.meta.url),
    'utf8',
  ),
);

/** Serves a published record without a backend so the page can be verified alone. */
async function mockRecordApi(page) {
  await page.route('**/api/v1/public/records', (route) =>
    route.fulfill({
      json: {
        items: [
          {
            publication_id: 'fixture-publication',
            commission_id: RECORD.commission.id,
            title: RECORD.commission.title,
            published_at: RECORD.commission.created_at,
          },
        ],
      },
    }),
  );
  await page.route('**/api/v1/public/records/*', (route) =>
    route.fulfill({ json: RECORD }),
  );
}

/*
 * Frontend verification for the VELIN page. Runs against the Vite dev server
 * (see playwright.config.mjs) in the installed Chrome, on desktop, tablet and
 * mobile projects. Nothing here talks to a backend.
 */

const IDLE_MS = 2500 + 1500; // CONFIG.idleThresholdMs + CONFIG.idleEaseInMs

/** Collects page errors, console errors and failed same-origin requests. */
function watch(page) {
  const errors = [];
  const broken = [];
  page.on('pageerror', (error) => errors.push(`pageerror: ${error.message}`));
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(`console: ${message.text()}`);
  });
  page.on('response', (response) => {
    const url = response.url();
    if (url.startsWith('http://127.0.0.1') && response.status() >= 400) {
      broken.push(`${response.status()} ${url}`);
    }
  });
  page.on('requestfailed', (request) => {
    const url = request.url();
    if (url.startsWith('http://127.0.0.1')) {
      broken.push(`failed ${url} ${request.failure()?.errorText ?? ''}`);
    }
  });
  return { errors, broken };
}

/** Resolves once both portraits have been bound to the shader. */
function portraitsLoaded(page) {
  const seen = [];
  const done = new Promise((resolve) => {
    page.on('console', (message) => {
      const text = message.text();
      if (text.startsWith('[VELIN] Loaded ')) {
        seen.push(text);
        if (seen.length === 2) resolve(seen);
      }
    });
  });
  return { seen, done };
}

/**
 * Reads the painted WebGL frame inside requestAnimationFrame. The renderer is
 * paced at 60Hz and does not preserve its drawing buffer, so a sample taken on
 * a skipped frame reads an empty buffer; those are retried.
 */
function sampleFrame(page) {
  return page.locator('.hero canvas').evaluate(
    (canvas) =>
      new Promise((resolve, reject) => {
        let frames = 0;
        const sample = async () => {
          const gl = canvas.getContext('webgl2');
          if (!gl) return reject(new Error('WebGL2 unavailable'));
          const w = gl.drawingBufferWidth;
          const h = gl.drawingBufferHeight;
          const pixels = new Uint8Array(w * h * 4);
          gl.readPixels(0, 0, w, h, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
          const shades = new Set(pixels).size;
          if (shades <= 100) {
            if (++frames >= 240) {
              return reject(new Error('No painted frame observed'));
            }
            requestAnimationFrame(sample);
            return;
          }
          const digest = await crypto.subtle.digest('SHA-256', pixels);
          const hash = Array.from(new Uint8Array(digest), (b) =>
            b.toString(16).padStart(2, '0'),
          ).join('');
          // Mean colour of a 9x9 block at the centre of the frame.
          const cx = Math.floor(w / 2);
          const cy = Math.floor(h / 2);
          const sum = [0, 0, 0];
          for (let y = cy - 4; y <= cy + 4; y++) {
            for (let x = cx - 4; x <= cx + 4; x++) {
              const i = (y * w + x) * 4;
              sum[0] += pixels[i];
              sum[1] += pixels[i + 1];
              sum[2] += pixels[i + 2];
            }
          }
          resolve({
            hash,
            shades,
            center: sum.map((v) => v / 81),
            at: performance.now(),
            size: [w, h],
          });
        };
        requestAnimationFrame(sample);
      }),
  );
}

/** Mean colour of the 9x9 block at the centre of a portrait file. */
function centreOf(page, url) {
  return page.evaluate(
    (src) =>
      new Promise((resolve, reject) => {
        const image = new Image();
        image.onload = () => {
          const canvas = document.createElement('canvas');
          canvas.width = image.naturalWidth;
          canvas.height = image.naturalHeight;
          const context = canvas.getContext('2d');
          context.drawImage(image, 0, 0);
          const cx = Math.floor(canvas.width / 2);
          const cy = Math.floor(canvas.height / 2);
          const data = context.getImageData(cx - 4, cy - 4, 9, 9).data;
          const sum = [0, 0, 0];
          for (let i = 0; i < data.length; i += 4) {
            sum[0] += data[i];
            sum[1] += data[i + 1];
            sum[2] += data[i + 2];
          }
          resolve(sum.map((v) => v / 81));
        };
        image.onerror = () => reject(new Error(`cannot load ${src}`));
        image.src = src;
      }),
    url,
  );
}

const distance = (a, b) => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]);

/** Draws a stroke through the centre with the mouse or a synthetic touch. */
async function stroke(page, touch) {
  const box = await page.locator('.hero canvas').boundingBox();
  const from = { x: box.x + box.width * 0.2, y: box.y + box.height * 0.3 };
  const to = { x: box.x + box.width * 0.8, y: box.y + box.height * 0.7 };
  if (!touch) {
    await page.mouse.move(from.x, from.y);
    await page.mouse.move(to.x, to.y, { steps: 30 });
    return;
  }
  await page.evaluate(
    async ({ from, to }) => {
      const canvas = document.querySelector('.hero canvas');
      const steps = 30;
      for (let i = 0; i <= steps; i++) {
        const x = from.x + ((to.x - from.x) * i) / steps;
        const y = from.y + ((to.y - from.y) * i) / steps;
        const point = new Touch({
          identifier: 1,
          target: canvas,
          clientX: x,
          clientY: y,
          pageX: x,
          pageY: y,
        });
        window.dispatchEvent(
          new TouchEvent('touchmove', {
            touches: [point],
            changedTouches: [point],
            bubbles: true,
            cancelable: true,
          }),
        );
        await new Promise((resolve) => requestAnimationFrame(resolve));
      }
    },
    { from, to },
  );
}

test('reveal, texture swap, idle trail, menu, sections, no errors', async ({
  page,
}, testInfo) => {
  const touch = Boolean(testInfo.project.use.hasTouch);
  const { errors, broken } = watch(page);
  const portraits = portraitsLoaded(page);
  await mockRecordApi(page);
  // Hold the idle trail until both portraits are ready. Loading time and GPU
  // scheduling are not part of the animation contract.
  await page.emulateMedia({ reducedMotion: 'reduce' });

  await page.goto('/');
  await expect(page.locator('.hero canvas')).toBeVisible();
  await expect(page.locator('.hero-title')).toContainText('Long work');
  await portraits.done;
  expect(portraits.seen).toHaveLength(2);

  // Before motion is enabled the top portrait shows, regardless of load time.
  const before = await sampleFrame(page);
  expect(before.shades).toBeGreaterThan(100);

  const topCentre = await centreOf(page, '/portrait_top.png');
  const bottomCentre = await centreOf(page, '/portrait_bottom.png');
  expect(distance(topCentre, bottomCentre)).toBeGreaterThan(40);
  expect(distance(before.center, topCentre)).toBeLessThan(
    distance(before.center, bottomCentre),
  );

  // A stroke through the centre paints the mask and swaps to the bottom
  // portrait where it passed.
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  await stroke(page, touch);
  const after = await sampleFrame(page);
  expect(after.hash).not.toBe(before.hash);
  expect(distance(after.center, bottomCentre)).toBeLessThan(
    distance(after.center, topCentre),
  );
  await page.screenshot({ path: testInfo.outputPath('hero-after-stroke.png') });

  // Idle auto-trail: require a changed rendered frame within a bounded window.
  // A fixed 500ms pair can land on a quiet part of the path or a GPU stall.
  await page.waitForTimeout(IDLE_MS + 400);
  const idleA = await sampleFrame(page);
  await expect
    .poll(async () => (await sampleFrame(page)).hash, {
      timeout: 10000,
      intervals: [500],
    })
    .not.toBe(idleA.hash);

  // Index menu: opens, traps focus in a11y tree, closes on link.
  await page.getByRole('button', { name: 'Index', exact: true }).click();
  await expect(page.locator('.nav-toggle')).toHaveAttribute(
    'aria-expanded',
    'true',
  );
  await expect(page.locator('#site-menu')).toBeVisible();
  await page
    .locator('#site-menu')
    .getByRole('link', { name: '01 Manifesto' })
    .click();
  await expect(page.locator('.nav-toggle')).toHaveAttribute(
    'aria-expanded',
    'false',
  );
  await expect(page.locator('#site-menu')).toBeHidden();
  await expect(page.locator('#about')).toHaveClass(/is-visible/);

  // Section reveals and the workflow record rendered from the demo seam.
  for (const id of ['commissions', 'capabilities', 'process', 'history']) {
    await page.locator(`#${id}`).scrollIntoViewIfNeeded();
    await expect(page.locator(`#${id}`)).toHaveClass(/is-visible/);
  }
  await expect(page.locator('.ledger li')).toHaveCount(RECORD.events.length);
  await expect(page.locator('.record-title')).toContainText('The Long Book');
  await expect(page.locator('.record-note')).toContainText(
    'Authoritative record',
  );
  await page.locator('#commission').scrollIntoViewIfNeeded();
  await expect(page.locator('#commission')).toHaveClass(/is-visible/);
  await expect(page.locator('#commission')).toHaveCSS('opacity', '1');

  // No horizontal overflow at this viewport.
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);

  await page.screenshot({
    path: testInfo.outputPath('full-page.png'),
    fullPage: true,
  });

  expect(errors).toEqual([]);
  expect(broken).toEqual([]);
});

test('resize keeps the canvas matched to the viewport', async ({ page }) => {
  const { errors } = watch(page);
  await mockRecordApi(page);
  await page.goto('/');
  await expect(page.locator('.hero canvas')).toBeVisible();

  for (const size of [
    { width: 1280, height: 720 },
    { width: 900, height: 1100 },
    { width: 1440, height: 900 },
  ]) {
    await page.setViewportSize(size);
    await page.waitForTimeout(150);
    const measured = await page.locator('.hero canvas').evaluate((canvas) => {
      const dpr = Math.min(window.devicePixelRatio, 2);
      return {
        width: canvas.width,
        height: canvas.height,
        expectedWidth: Math.floor(window.innerWidth * dpr),
        expectedHeight: Math.floor(window.innerHeight * dpr),
      };
    });
    expect(Math.abs(measured.width - measured.expectedWidth)).toBeLessThan(3);
    expect(Math.abs(measured.height - measured.expectedHeight)).toBeLessThan(3);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
  expect(errors).toEqual([]);
});

test('reduced motion: no trail, no marquee, sections readable', async ({
  page,
}, testInfo) => {
  const { errors } = watch(page);
  const portraits = portraitsLoaded(page);
  await mockRecordApi(page);
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await page.goto('/');

  await expect(page.locator('#about')).toHaveClass(/is-visible/);
  await expect(page.locator('#commission')).toHaveClass(/is-visible/);
  expect(
    await page
      .locator('.marquee-track')
      .evaluate((el) => getComputedStyle(el).animationName),
  ).toBe('none');

  await portraits.done;
  const before = await sampleFrame(page);
  expect(before.shades).toBeGreaterThan(100);

  // Neither pointer input nor the idle clock may paint a trail.
  await stroke(page, Boolean(testInfo.project.use.hasTouch));
  const afterStroke = await sampleFrame(page);
  expect(afterStroke.hash).toBe(before.hash);

  await page.waitForTimeout(IDLE_MS + 600);
  const afterIdle = await sampleFrame(page);
  expect(afterIdle.hash).toBe(before.hash);

  await page.screenshot({ path: testInfo.outputPath('reduced-motion.png') });
  expect(errors).toEqual([]);
});

test('an unreachable workflow service shows a controlled error, never demo data', async ({
  page,
}) => {
  const { errors } = watch(page);
  await page.route('**/api/v1/public/records', (route) =>
    route.fulfill({
      status: 503,
      json: {
        code: 'DEPENDENCY_UNAVAILABLE',
        message: 'A required dependency is unavailable.',
        traceId: '0',
      },
    }),
  );
  await page.goto('/');
  await page.locator('#history').scrollIntoViewIfNeeded();
  const notice = page.locator('#record .record-error');
  await expect(notice).toContainText('DEPENDENCY_UNAVAILABLE');
  await expect(page.locator('.ledger li')).toHaveCount(0);
  await expect(page.locator('#record')).not.toContainText('C-0007');
  // The intentional 503 produces the browser's own resource error line and the
  // page's controlled log entry; nothing else may be reported.
  expect(
    errors.filter(
      (e) =>
        !e.includes('workflow record could not be rendered') &&
        !e.includes('Failed to load resource'),
    ),
  ).toEqual([]);
});
