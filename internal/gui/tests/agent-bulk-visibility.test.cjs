// Visibility changes use one arrangement write and never touch agent configs.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { test } = require('node:test');
const { chromium, webkit } = require('playwright');
const assets = process.env.MAGPIE_AGENT_ASSETS || path.resolve(__dirname, '../assets');
const names = {
  en: ['Manage agents', 'Hide selected', 'Unhide selected', 'Select search results', 'Invert search selection', 'Select unconnected agents', 'Clear selection'],
  zh: ['批量管理', '隐藏所选', '取消隐藏所选', '全选搜索结果', '反选搜索结果', '选择未接入项', '清空选择'],
  'zh-TW': ['批次管理', '隱藏所選項目', '取消隱藏所選項目', '全選搜尋結果', '反選搜尋結果', '選取未連線項目', '清除選取'],
  ja: ['エージェントを一括管理', '選択項目を非表示', '選択項目の非表示を解除', '検索結果をすべて選択', '検索結果の選択を反転', '未接続のエージェントを選択', '選択を解除'],
  de: ['Agents verwalten', 'Auswahl ausblenden', 'Auswahl einblenden', 'Suchergebnisse auswählen', 'Auswahl der Suchergebnisse umkehren', 'Nicht verbundene Agents auswählen', 'Auswahl aufheben'],
};
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit']) {
  for (const lang of Object.keys(names)) {
    test(`${engine} ${lang}: bulk visibility, retry and persistence`, async (t) => {
      const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({ channel: 'chromium' }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: lang === 'en' ? 1240 : 560, height: 760 }, reducedMotion: 'reduce' });
      page.setDefaultTimeout(5000);
      const agent = (id, wired = false) => ({ id, name: id, icon: 'generic', wired, fields: [{ key: 'model', label: 'model', value: wired ? 'relay/m1' : '', options: [{ value: 'relay/m1', label: 'm1', ref: 'relay/m1' }] }] });
      const theme = lang === 'de' ? 'dark' : 'light';
      const state = { agents: [agent('Codex', true), agent('Pi'), agent('Zed'), agent('Hidden')], profiles: [], settings: { lang, theme, agentOrder: ['Zed', 'Codex', 'Pi', 'Hidden'], agentsHidden: ['Hidden'] } };
      const posts = [], errors = [];
      let reject = false;
      page.on('pageerror', (e) => errors.push(e.message));
      await page.route('**/*', async (route) => {
        const req = route.request(), url = new URL(req.url());
        const json = (data) => route.fulfill({ json: data });
        if (req.method() === 'POST') posts.push([url.pathname, req.postDataJSON()]);
        if (url.pathname === '/boot.js') return route.fulfill({ contentType: 'text/javascript', body: `window.bootPrefs = ${JSON.stringify({lang, theme, web:true})};` });
        if (url.pathname === '/wails/runtime.js') return route.fulfill({ contentType: 'text/javascript', body: 'export const Window = {};' });
        if (url.pathname === '/api/state') return json(state);
        if (url.pathname === '/api/agents/arrange') {
          if (reject) return route.fulfill({ status: 500, json: { error: 'Save refused' } });
          await new Promise((r) => setTimeout(r, 80));
          const b = req.postDataJSON();
          Object.assign(state.settings, { agentOrder: b.order, agentsHidden: b.hidden, agentsShown: b.shown });
          return json(state.settings);
        }
        if (url.pathname === '/api/plugins') return json({ plugins: [] });
        if (url.pathname === '/api/usage/quotas') return json([]);
        if (url.pathname === '/api/groups') return json({ groups: [] });
        if (url.pathname === '/api/agents/cli') return json({ agents: {}, pending: false });
        if (url.pathname === '/api/providers') return json({ providers: [], presets: [], gateway: { running: true } });
        if (url.pathname.startsWith('/api/')) return json({});
        const file = path.join(assets, url.pathname === '/' ? 'index.html' : url.pathname);
        try { return await route.fulfill({ body: await fs.readFile(file), contentType: {'.js':'text/javascript','.css':'text/css','.html':'text/html','.svg':'image/svg+xml'}[path.extname(file)] }); }
        catch { return route.fulfill({ status: 404, body: '' }); }
      });
      await page.goto('http://magpie.test/');
      const [manage, hide, show, all, invert, unconnected, clear] = names[lang];
      const footer = page.locator('#agents > .ag-manage-bar');
      await footer.waitFor();
      assert.equal(await page.locator('#view-agents > .ag-manage-bar').count(), 0, 'no separate toolbar above the list');
      assert.equal(await footer.locator('.agent-more').count(), 1, 'fold and manage share the footer');
      for (const width of [1240, 360]) {
        await page.setViewportSize({width, height:760});
        const boxes = await footer.evaluate((f) => {
          const more = f.querySelector('.agent-more').getBoundingClientRect(), manage = f.querySelector('.ag-manage-button').getBoundingClientRect();
          return {right:more.right, left:manage.left, bottom:more.bottom, top:manage.top, overflow:f.scrollWidth > f.clientWidth};
        });
        assert(boxes.right <= boxes.left, 'footer controls do not overlap');
        assert(boxes.bottom > boxes.top, 'footer controls share a row');
        assert.equal(boxes.overflow, false);
      }
      await page.setViewportSize({width:lang === 'en' ? 1240 : 560, height:760});
      if (process.env.FOOTER_SHOT && lang === 'zh') await page.screenshot({path:process.env.FOOTER_SHOT});
      await page.getByRole('button', { name: manage, exact: true }).click();
      const dialog = page.getByRole('dialog'), search = dialog.locator('input[type=search]');
      const button = (name) => dialog.getByRole('button', { name, exact: true });
      assert.equal(await dialog.getByRole('checkbox').count(), 4, 'hidden and folded agents are manageable');
      assert(await button(hide).isDisabled());
      await search.fill('not-an-agent');
      assert.equal(await dialog.getByRole('checkbox').count(), 0);
      await search.fill('Pi');
      await button(all).click();
      await button(invert).click();
      assert.equal(await dialog.locator('input:checked').count(), 0);
      await button(all).click();
      await search.fill('');
      assert.equal(await dialog.locator('input:checked').count(), 1, 'search preserves selection');
      await button(clear).click();
      await button(unconnected).click();
      assert.equal(await dialog.locator('input:checked').count(), 3);
      assert.equal(await dialog.getByRole('checkbox', {name:'Codex',exact:true}).isChecked(), false);
      reject = true;
      await button(hide).click();
      await page.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === 'Save refused');
      assert.deepEqual(state.settings.agentsHidden, ['Hidden']);
      assert.equal(await dialog.locator('input:checked').count(), 3, 'failed save keeps selection');
      reject = false;
      await button(hide).click();
      await page.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent.includes('2'));
      assert.deepEqual(new Set(state.settings.agentsHidden), new Set(['Hidden', 'Pi', 'Zed']));
      assert.deepEqual(state.settings.agentOrder, ['Zed', 'Codex', 'Pi', 'Hidden']);
      assert.equal(posts.length, 2, 'one write per attempt');
      assert(posts.every(([url]) => url === '/api/agents/arrange'), 'no agent connection writes');
      await page.reload();
      await page.getByRole('button', {name:manage,exact:true}).click();
      await dialog.getByRole('checkbox', {name:'Pi',exact:true}).check();
      await dialog.getByRole('checkbox', {name:'Zed',exact:true}).check();
      assert(await button(hide).isDisabled(), 'saved hidden state survives reload');
      await button(show).click();
      await page.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent.includes('2'));
      assert.deepEqual(state.settings.agentsHidden, ['Hidden']);
      assert.equal(state.agents[0].wired, true);
      assert.equal(await dialog.locator('input:checked').count(), 0);
      assert(await dialog.evaluate((d) => d.scrollWidth <= d.clientWidth), 'narrow dialog does not overflow');
      // Hiding every agent keeps the manager available; connected agents stay wired.
      await button(all).click();
      await button(hide).click();
      await page.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent.includes('3'));
      assert.equal(state.settings.agentsHidden.length, 4);
      assert.equal(state.agents[0].wired, true);
      await page.keyboard.press('Escape');
      await page.locator('#modal[hidden]').waitFor({ state: 'attached' });
      await page.getByRole('button', {name:manage,exact:true}).click();
      await button(all).click();
      await button(show).click();
      await page.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent.includes('4'));
      assert.deepEqual(state.settings.agentsHidden, []);
      assert.equal(posts.length, 5);
      if (process.env.BULK_SHOT && lang === 'zh') {
        await dialog.evaluate(async (d) => { await Promise.all(d.parentElement.getAnimations({subtree:true}).map((a) => a.finished.catch(() => {}))); });
        await page.screenshot({path:process.env.BULK_SHOT});
      }
      state.agents.forEach((a) => { a.wired = true; });
      await page.reload();
      await page.getByRole('button', {name:manage,exact:true}).waitFor();
      assert.equal(await footer.locator('.agent-more').count(), 0, 'no fold control when every agent is shown');
      assert.equal(await footer.locator('.ag-manage-button').count(), 1, 'management remains available without folded agents');
      assert.deepEqual(errors, []);
    });
  }
}
