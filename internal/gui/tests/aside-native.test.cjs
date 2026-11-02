const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { test } = require('node:test');
const { chromium, webkit } = require('playwright');
const assets = path.resolve(__dirname, '../assets');

for (const engine of ['chromium', 'webkit']) {
  for (const lang of ['en', 'zh']) {
    test(`${engine} ${lang}: Aside provider connection is independent of model selection`, async (t) => {
      const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({ channel: 'chromium' }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 980, height: 760 }, reducedMotion: 'reduce' });
      const posts = [], errors = [];
      page.on('pageerror', (e) => errors.push(e.message));
      const a = {
        id: 'aside', name: 'Aside', icon: 'aside', path: '~/.aside/u/0/settings.json',
        native: { provider: 'disconnected', runtime: 'applied', fields: { image: { value: 'magpie/art/gpt-image-1', status: 'unverified', detail: "Image generation is configured, but Aside's image provider support has not been verified" } } },
        fields: [
          { key: 'model', label: 'model', value: 'minimax/native', options: [{ value: 'minimax/native', label: 'Native MiniMax' }, { value: 'magpie/relay/m1', ref: 'relay/m1', label: 'Magpie model' }] },
          { key: 'image', label: 'image', value: 'magpie/art/gpt-image-1', options: [{ value: 'magpie/art/gpt-image-1', ref: 'art/gpt-image-1', label: 'GPT image' }] },
        ],
      };
      const state = () => ({ agents: [a], profiles: [], settings: { lang, theme: 'light', guideHidden: true } });
      await page.route('**/*', async (route) => {
        const req = route.request(), url = new URL(req.url()), p = url.pathname;
        if (req.method() === 'POST') posts.push(p);
        if (p === '/boot.js') return route.fulfill({ contentType: 'text/javascript', body: `window.bootPrefs=${JSON.stringify({ lang, theme: 'light', web: true })}` });
        if (p === '/api/state') return route.fulfill({ json: state() });
        if (p === '/api/agents/connect/aside') { a.wired = true; a.native.provider = 'connected'; return route.fulfill({ json: { ...state(), connected: { how: 'joined' } } }); }
        if (p === '/api/agents/preview/aside') return route.fulfill({ json: { revision: "provider-plan", changes: [{ path: '~/.aside/u/0/models.json', lines: [{ op: '-', text: 'providers.magpie' }] }] } });
        if (p === '/api/agents/disconnect/aside') { a.wired = false; a.native.provider = 'disconnected'; return route.fulfill({ json: state() }); }
        if (p === '/api/providers') return route.fulfill({ json: { providers: [], presets: [], gateway: { running: true } } });
        if (p === '/api/groups') return route.fulfill({ json: { groups: [] } });
        if (p === '/api/plugins') return route.fulfill({ json: { plugins: [] } });
        if (p === '/api/usage/quotas') return route.fulfill({ json: [] });
        if (p === '/api/agents/cli') return route.fulfill({ json: { agents: {}, pending: false } });
        if (p.startsWith('/api/')) return route.fulfill({ json: {} });
        const file = path.join(assets, p === '/' ? 'index.html' : p);
        return route.fulfill({ body: await fs.readFile(file), contentType: { '.js': 'text/javascript', '.html': 'text/html', '.css': 'text/css', '.svg': 'image/svg+xml' }[path.extname(file)] });
      });
      await page.goto('http://magpie.test/?view=agents');
      const row = page.locator('.row.agent[data-id="aside"]');
      await row.locator('.ag-start').waitFor();
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      await row.locator('.ag-conn').click();
      await page.waitForFunction(() => document.querySelector('[data-id="aside"] .ag-conn')?.getAttribute('aria-checked') === 'true');
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      assert.equal(await row.locator('.ag-fix').count(), 0, 'image warning does not offer a provider reconnect');
      await row.locator('.ag-link').click();
      await row.locator('.ag-exp').waitFor();
      assert.match(await row.locator('.ag-exp').textContent(), lang === 'zh' ? /已配置图片生成/ : /Image generation is configured/);
      assert.deepEqual(posts, ['/api/agents/connect/aside'], 'opening details sends no mutation');
      await row.locator('.ag-conn').click();
      const ask = page.locator('.disconnect-ask');
      await ask.waitFor();
      await ask.locator('.bar button').last().click();
      await ask.waitFor({ state: 'detached' });
      assert.match(await row.locator('.ag-start').textContent(), /Native MiniMax/);
      assert.deepEqual(posts, ['/api/agents/connect/aside', '/api/agents/disconnect/aside']);
      assert.deepEqual(errors, []);
    });
  }
}

for (const engine of ['chromium', 'webkit']) {
  for (const lang of ['en', 'zh']) {
    test(`${engine} ${lang}: unavailable operation requires a separate offline confirmation`, async (t) => {
      const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({ channel: 'chromium' }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 980, height: 760 } });
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const posts = [], errors = [];
      page.on('pageerror', e => errors.push(e.message));
      let failure = { error: 'Aside settings could not be read', code: 'runtime_unavailable', offline: 'stage' };
      let revision = 'confirmed-plan';
      let failPreview = false, releasePreview = null, blockPreview = false;
      const a = {
        id: 'aside', name: 'Aside', icon: 'aside', wired: true, path: '~/.aside/u/0/settings.json',
        native: { provider: 'connected', runtime: 'applied', fields: {} },
        fields: [{ key: 'model', label: 'model', value: 'native/current', options: [{ value: 'native/current', label: 'Current model' }, { value: 'magpie/relay/m1', ref: 'relay/m1', label: 'New model' }] }],
      };
      const current = () => ({ agents: [a], profiles: [], settings: { lang, theme: 'light', guideHidden: true } });
      await page.route('**/*', async route => {
        const req = route.request(), p = new URL(req.url()).pathname;
        if (req.method() === 'POST') posts.push({ path: p, body: req.postDataJSON() });
        if (p === '/boot.js') return route.fulfill({ contentType: 'text/javascript', body: `window.bootPrefs=${JSON.stringify({ lang, theme: 'light', web: true })}` });
        if (p === '/api/state') return route.fulfill({ json: current() });
        if (p === '/api/set') return route.fulfill({ status: 400, json: failure });
        if (p === '/api/agents/stage/aside') { a.fields[0].value = req.postDataJSON().value; return route.fulfill({ json: current() }); }
        if (p === '/api/agents/preview/aside') {
          if (blockPreview) await new Promise(resolve => { releasePreview = resolve; });
          return route.fulfill({ json: failPreview ? { error: 'No restore point' } : { revision, changes: [{ path: a.path, lines: [{ op: '~', text: 'native/current', was: 'magpie/relay/m1' }] }] } });
        }
        if (p === '/api/agents/disconnect/aside') return route.fulfill({ status: 400, json: { error: 'Aside unavailable', code: 'runtime_unavailable', offline: 'disconnect' } });
        if (p === '/api/agents/disconnect-offline/aside') {
          if (req.postDataJSON().revision !== revision) return route.fulfill({ status: 400, json: { error: 'Disconnect preview changed; preview it again' } });
          a.wired = false; a.native.provider = 'disconnected'; return route.fulfill({ json: current() });
        }
        if (p === '/api/providers') return route.fulfill({ json: { providers: [], presets: [], gateway: { running: true } } });
        if (p === '/api/groups') return route.fulfill({ json: { groups: [] } });
        if (p === '/api/plugins') return route.fulfill({ json: { plugins: [] } });
        if (p === '/api/usage/quotas') return route.fulfill({ json: [] });
        if (p === '/api/agents/cli') return route.fulfill({ json: { agents: {}, pending: false } });
        if (p.startsWith('/api/')) return route.fulfill({ json: {} });
        const file = path.join(assets, p === '/' ? 'index.html' : p);
        return route.fulfill({ body: await fs.readFile(file), contentType: { '.js': 'text/javascript', '.html': 'text/html', '.css': 'text/css', '.svg': 'image/svg+xml' }[path.extname(file)] });
      });
      await page.goto('http://magpie.test/?view=agents');
      const row = page.locator('.row.agent[data-id="aside"]');
      await row.locator('.ag-start').waitFor();
      const pick = () => page.evaluate(() => setPick(state.agents[0], state.agents[0].fields[0], 'magpie/relay/m1'));
      await pick();
      const ask = page.locator('.disconnect-ask');
      await ask.waitFor();
      assert.equal(posts.filter(p => p.path.includes('/stage/')).length, 0);
      await ask.locator('.bar button').first().click();
      await ask.waitFor({ state: 'detached' });
      assert.match(await row.locator('.ag-start').textContent(), /Current model/);
      await pick();
      await ask.waitFor();
      await page.keyboard.press('Escape');
      await ask.waitFor({ state: 'detached' });
      assert.equal(posts.filter(p => p.path.includes('/stage/')).length, 0);
      failure = { error: 'Generic refusal' };
      await pick();
      assert.equal(await ask.count(), 0, 'generic refusal offers no fallback');
      failure = { error: 'Effort unavailable', code: 'runtime_unavailable', offline: '' };
      await pick();
      assert.equal(await ask.count(), 0, 'unsupported action offers no fallback');
      failure = { error: 'Offline', code: 'runtime_unavailable', offline: 'stage' };
      await pick();
      await ask.waitFor();
      await ask.locator('.bar button').last().click();
      await ask.waitFor({ state: 'detached' });
      assert.deepEqual(posts.filter(p => p.path.includes('/stage/')), [{ path: '/api/agents/stage/aside', body: { field: 'model', value: 'magpie/relay/m1' } }]);
      const disconnect = async () => {
        await page.evaluate(() => askDisconnect(state.agents[0]));
        await ask.waitFor();
        const go = ask.locator('.bar button').last();
        await page.waitForFunction(() => !document.querySelector('.disconnect-ask .bar button:last-child').disabled);
        await go.click();
        await page.waitForFunction(() => document.querySelector('.disconnect-ask')?.textContent.includes('Close Aside') || document.querySelector('.disconnect-ask')?.textContent.includes('关闭 Aside'));
      };
      blockPreview = true;
      await page.evaluate(() => askDisconnect(state.agents[0]));
      await ask.waitFor();
      assert.equal(await ask.locator('.bar button').last().isDisabled(), true, 'wait for successful preview');
      while (!releasePreview) await new Promise(resolve => setTimeout(resolve, 10));
      blockPreview = false; releasePreview();
      await page.waitForFunction(() => !document.querySelector('.disconnect-ask .bar button:last-child').disabled);
      await ask.locator('.bar button').first().click();
      await ask.waitFor({ state: 'detached' });
      await disconnect();
      await ask.locator('.bar button').first().click();
      await ask.waitFor({ state: 'detached' });
      assert.equal(posts.filter(p => p.path.includes('/disconnect-offline/')).length, 0);
      await disconnect();
      revision = 'changed-plan';
      await ask.locator('.bar button').last().click();
      await page.waitForFunction(() => !document.querySelector('.disconnect-ask .bar button:last-child').disabled);
      assert.equal(a.wired, true, 'stale preview retains provider');
      await ask.locator('.bar button').first().click();
      await ask.waitFor({ state: 'detached' });
      failPreview = true;
      await page.evaluate(() => askDisconnect(state.agents[0]));
      await page.waitForFunction(() => document.querySelector('.disconnect-ask')?.textContent.includes('No restore point'));
      assert.equal(await ask.locator('.bar button').last().isDisabled(), true);
      await ask.locator('.bar button').first().click();
      failPreview = false;
      await disconnect();
      await ask.locator('.bar button').last().click();
      await ask.waitFor({ state: 'detached' });
      assert.equal(a.wired, false, 'offline execution works with missing CLI');
      assert.deepEqual(errors, []);
    });
  }
}
