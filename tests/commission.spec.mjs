import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';

/*
 * Browser flow through the private folio against the real local stack: open
 * with a local token, submit a brief, watch execution reach review, reload
 * and resume the same durable folio, approve the exact revision, read the
 * provenance and download the artifact. Enabled with E2E_STACK=1.
 */

test.skip(
  process.env.E2E_STACK !== '1',
  'set E2E_STACK=1 with the local stack running',
);

test('a commission runs from brief to a bound approval and a delivered artifact', async ({
  page,
}, testInfo) => {
  const access = JSON.parse(
    await readFile(resolve('.local/test-access.json'), 'utf8'),
  );
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  page.on('console', (message) => {
    if (message.type() === 'error') errors.push(message.text());
  });
  await page.goto('/');
  await page
    .locator('#commission')
    .getByRole('link', { name: 'Start a workflow' })
    .click();
  await expect(page).toHaveURL(/\/commission\/$/);
  await page.getByLabel('Access token').fill(access.token);
  await page.getByRole('button', { name: 'Open my folio' }).click();
  await expect(page.locator('#workspace')).toBeVisible();
  await expect(page.locator('#provider-notice')).toContainText(
    'Deterministic capability fixtures',
  );
  const title = `Browser ${testInfo.project.name} ${Date.now()}`;
  await page.getByLabel('Commission title').fill(title);
  await page
    .getByLabel('The intention')
    .fill(
      'Create an editorial identity for a cultural journal with clear hierarchy.',
    );
  await page.getByLabel('The audience').fill('Readers of contemporary culture');
  await page
    .getByLabel('Hard constraints')
    .fill('Accessible contrast\nRespect reduced motion');
  await page.getByLabel('Patterns to avoid').fill('neon gradients');
  await page.getByRole('button', { name: 'Submit the brief' }).click();
  await expect(page.locator('#process-section')).toBeVisible();
  await expect(page.locator('#commission-title')).toHaveText(title);
  await expect(page.locator('#review-section')).toBeVisible({ timeout: 90000 });
  await expect(page.locator('#direction-content')).toContainText('Typography');
  await expect(page.locator('#approval-binding')).toContainText('for revision');
  await page.screenshot({
    path: testInfo.outputPath('review.png'),
    fullPage: true,
  });

  // Reload abandons browser memory; the same durable folio resumes.
  const workflowURL = page.url();
  await page.reload();
  await expect(page.locator('#access-section')).toBeVisible();
  await page.getByLabel('Access token').fill(access.token);
  await page.getByRole('button', { name: 'Open my folio' }).click();
  await expect(page).toHaveURL(workflowURL);
  await expect(page.locator('#review-section')).toBeVisible();
  await page
    .getByLabel('Review notes')
    .fill(
      'Approved by the authorized local test operator to verify the end-to-end workflow.',
    );
  await page.getByRole('button', { name: 'Approve this revision' }).click();
  await expect(page.locator('#stage-description')).toContainText('completed', {
    timeout: 90000,
  });
  await expect(page.locator('#run-label')).toContainText('1 artifact');
  await page.getByRole('button', { name: 'Read the provenance' }).click();
  await expect(page.locator('#provenance')).toBeVisible();
  const lineage = JSON.parse(
    (await page.locator('#provenance').textContent()) || '{}',
  );
  expect(lineage.steps.length).toBeGreaterThanOrEqual(8);
  expect(lineage.decisions).toHaveLength(1);
  expect(lineage.decisions[0].action).toBe('APPROVE');
  expect(lineage.decisions[0].revision_id).toBe(
    lineage.approvals[0].revision_id,
  );
  expect(lineage.artifacts[0].revision_id).toBe(
    lineage.decisions[0].revision_id,
  );
  expect(
    lineage.provenance.some(
      (row) =>
        row.capability === 'artifact' &&
        row.decision_id === lineage.decisions[0].decision_id,
    ),
  ).toBe(true);
  expect(
    lineage.steps.every(
      (step) =>
        step.provenance.live === false &&
        step.provenance.prompt_hash.length === 64,
    ),
  ).toBe(true);
  const downloadPromise = page.waitForEvent('download');
  await page.getByRole('button', { name: 'Download the artifact' }).click();
  const download = await downloadPromise;
  await download.saveAs(testInfo.outputPath('artifact.json'));
  await page.screenshot({
    path: testInfo.outputPath('delivered.png'),
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  expect(errors).toEqual([]);
  await page.getByRole('button', { name: 'Close folio' }).click();
  await expect(page.locator('#access-section')).toBeVisible();
  await expect(page.locator('#provenance')).toBeEmpty();
});
