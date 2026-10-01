import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile, mkdir } from 'node:fs/promises';
import { chromium } from 'playwright';

let browser, server, origin;
before(async () => {
  server = createServer(async (req, res) => {
    const path = new URL(req.url, 'http://localhost').pathname;
    const name = path === '/' ? 'index.html' : path.replace(/^\/admin\//, '');
    if (!['index.html', 'app.css', 'enhancements.css', 'app.js', 'app-core.mjs'].includes(name)) return res.writeHead(404).end();
    res.setHeader('Content-Type', name.endsWith('html') ? 'text/html' : name.endsWith('css') ? 'text/css' : 'text/javascript');
    res.end(await readFile(new URL(`../web/${name}`, import.meta.url)));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch({ headless: true });
});
after(async () => { await browser?.close(); if (server) await new Promise(resolve => server.close(resolve)); });

// Real production HTML/CSS/JS in Chromium; API fixtures deliberately avoid live mutations.
async function setup(t, width = 1440) {
  const page = await browser.newPage({ viewport: { width, height: 1000 }, reducedMotion: 'reduce' });
  t.after(() => page.close());
  let authenticated = false;
  await page.route('**/api/admin/v1/**', route => {
    const path = new URL(route.request().url()).pathname;
    const respond = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body), headers: { ETag: '"design-fixture"' } });
    if (path.endsWith('/auth/login')) { authenticated = true; return respond({ csrfToken: 'design-fixture' }); }
    if (!authenticated) return respond({}, 401);
    if (path.endsWith('/auth/me')) return respond({ csrfToken: 'design-fixture' });
    if (path.endsWith('/config')) return respond({ mcpServers: { filesystem: { type: 'stdio', command: 'node', args: ['server.mjs'] } } });
    if (path.endsWith('/tokens')) return respond({ tokens: [{ index: 0, token: 'design-token-000000000000000000000000000001', legacy: true }] });
    if (path.endsWith('/status')) return respond({ version: 'design-fixture', servers: [{ id: 'filesystem', state: 'ready', publishedToolCount: 4 }], recentCalls: [{ tool: 'filesystem.read_file', serverId: 'filesystem', outcome: 'success', durationMs: 24, time: '2026-09-07T10:00:00Z' }] });
    return respond({});
  });
  await page.goto(origin);
  if (process.env.DESIGN_SCREENSHOTS) {
    await mkdir(process.env.DESIGN_SCREENSHOTS, { recursive: true });
    await page.screenshot({ path: `${process.env.DESIGN_SCREENSHOTS}/login-${width}.png` });
  }
  await page.locator('#token').fill('design-fixture');
  await page.locator('#loginButton').click();
  await page.locator('#app').waitFor({ state: 'visible' });
  await page.locator('[data-action="edit"]').waitFor();
  if (process.env.DESIGN_SCREENSHOTS) await page.screenshot({ path: `${process.env.DESIGN_SCREENSHOTS}/console-${width}.png`, fullPage: true });
  return page;
}

test('reduced motion disables smooth scrolling and button transitions', async t => {
  const page = await setup(t);
  assert.equal(await page.evaluate(() => getComputedStyle(document.documentElement).scrollBehavior), 'auto');
  assert.equal(await page.locator('#addServer').evaluate(el => getComputedStyle(el).transitionDuration), '0s');
});

test('editor switch exposes a visible keyboard focus ring', async t => {
  const page = await setup(t);
  await page.locator('#addServer').click();
  await page.locator('#serverType').focus();
  await page.keyboard.press('Tab');
  assert.equal(await page.locator('#enabled').evaluate(el => el === document.activeElement), true);
  const ring = await page.locator('.switch-row i').evaluate(el => ({ width: getComputedStyle(el).outlineWidth, style: getComputedStyle(el).outlineStyle }));
  assert.notEqual(ring.style, 'none');
  assert.ok(parseFloat(ring.width) >= 2);
});

test('search, filters and page-size controls have persistent accessible names', async t => {
  const page = await setup(t);
  for (const id of ['serverSearch', 'callSearch', 'callOutcome']) assert.ok(await page.locator(`#${id}`).getAttribute('aria-label'));
  assert.equal(await page.locator('#serverPageSize').isVisible(), true);
  assert.equal(await page.locator('#callPageSize').isVisible(), true);
});

test('client access combines tokens, manual config and local Agent setup', async t => {
  const page = await setup(t);
  assert.equal(await page.locator('#accessTokenSelect').isVisible(), true);
  assert.match(await page.locator('#mcpEndpoint').textContent(), /\/mcp\/progressive$/);
  const hint = await page.locator('.connection-card').filter({has: page.locator('#mcpEndpoint')}).locator('p').textContent();
  assert.match(hint, /hub_search_tools.*hub_describe_tool.*hub_call_tool/);
  assert.match(hint, /full/);
  assert.match(hint, /审批/);
  assert.match(await page.locator('#httpAuthorization').textContent(), /^Authorization: Bearer /);
  assert.equal(await page.locator('a[href="/api/admin/v1/client-skill.zip"]').isVisible(), true);
  assert.equal(await page.locator('#copyClientPrompt').isVisible(), true);
  assert.equal(await page.locator('text=Agent 自动部署').count(), 0);
});

for (const width of [320, 390, 768, 1024, 1440]) {
  test(`all sections and call columns preserved without page overflow at ${width}px`, async t => {
    const page = await setup(t, width);
    for (const id of ['overview', 'serversSection', 'callsSection', 'connectSection']) assert.equal(await page.locator(`#${id}`).isVisible(), true, id);
    assert.equal(await page.locator('.call-table th').nth(1).isVisible(), true);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'page overflows horizontally');
    for (const item of await page.locator('.nav-item').all()) {
      await item.click();
      const id = await item.getAttribute('data-section');
      assert.equal(await page.locator(`#${id}`).isVisible(), true);
    }
    await page.locator('#addServer').click();
    assert.ok(await page.locator('#editor').evaluate(el => el.scrollWidth <= el.clientWidth), 'dialog overflows horizontally');
    await page.locator('#cancelEditor').click();
  });
}
