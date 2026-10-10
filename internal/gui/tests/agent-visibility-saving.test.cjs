// Deferred responses exercise close paths and state reads across a visibility save.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const {test} = require('node:test');
const {chromium, webkit} = require('playwright');
const assets = path.resolve(__dirname, '../assets');
const deferred = () => { let resolve; const promise = new Promise((r) => { resolve = r; }); return {promise, resolve}; };
async function fixture(t, engine) {
  const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({channel:'chromium'}));
  t.after(() => browser.close());
  const page = await browser.newPage({viewport:{width:1000,height:800}, reducedMotion:'reduce'});
  page.setDefaultTimeout(5000);
  const agent = (id, wired = false) => ({id, name:id, icon:'generic', wired, fields:[{key:'model',label:'model',value:wired ? 'relay/m1' : '',options:[{value:'relay/m1',label:'m1',ref:'relay/m1'}]}]});
  const f = {page, state:{agents:[agent('Main',true),agent('Pi'),agent('Zed')],profiles:[],settings:{lang:'en',theme:'light',agentsHidden:['Pi']}}, posts:[], save:null, read:null};
  await page.route('**/*', async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({json:data});
    if (url.pathname === '/boot.js') return route.fulfill({contentType:'text/javascript',body:'window.bootPrefs = {lang:"en",theme:"light",web:true};'});
    if (url.pathname === '/wails/runtime.js') return route.fulfill({contentType:'text/javascript',body:'export const Window = {};'});
    if (url.pathname === '/api/state') {
      const snapshot = JSON.parse(JSON.stringify(f.state));
      const gate = f.read; f.read = null;
      if (gate) { gate.started.resolve(); await gate.release.promise; }
      return json(snapshot);
    }
    if (url.pathname === '/api/agents/arrange') {
      const b = req.postDataJSON(); f.posts.push(b);
      const gate = f.save; f.save = null;
      if (gate) { gate.started.resolve(); await gate.release.promise; }
      if (gate?.fail) return route.fulfill({status:500,json:{error:'Save refused'}});
      Object.assign(f.state.settings,{agentOrder:b.order,agentsHidden:b.hidden,agentsShown:b.shown});
      return json(f.state.settings);
    }
    if (url.pathname === '/api/plugins') return json({plugins:[]});
    if (url.pathname === '/api/groups') return json({groups:[]});
    if (url.pathname === '/api/usage/quotas') return json([]);
    if (url.pathname === '/api/agents/cli') return json({agents:{},pending:false});
    if (url.pathname === '/api/providers') return json({providers:[],presets:[],gateway:{running:true}});
    if (url.pathname.startsWith('/api/')) return json({});
    const file = url.pathname === '/app.js' && process.env.AGENT_VISIBILITY_APP
      ? process.env.AGENT_VISIBILITY_APP : path.join(assets,url.pathname === '/' ? 'index.html' : url.pathname);
    try { return await route.fulfill({body:await fs.readFile(file),contentType:{'.js':'text/javascript','.css':'text/css','.html':'text/html','.svg':'image/svg+xml'}[path.extname(file)]}); }
    catch { return route.fulfill({status:404,body:''}); }
  });
  await page.goto('http://magpie.test/');
  await page.getByRole('button',{name:'Manage agents',exact:true}).click();
  return f;
}
const gate = () => ({started:deferred(),release:deferred()});
const close = async (page) => { await page.keyboard.press('Escape'); await page.locator('#modal[hidden]').waitFor({state:'attached'}); };
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ['chromium','webkit']) {
  test(`${engine}: visibility counts use singular for one agent`, async (t) => {
    const {page:p} = await fixture(t,engine);
    const count = p.locator('.ag-manage-count');
    assert.equal(await count.textContent(),'0 agents selected');
    await p.getByRole('checkbox',{name:'Pi',exact:true}).check();
    assert.equal(await count.textContent(),'1 agent selected');
    await p.getByRole('button',{name:'Unhide selected',exact:true}).click();
    await p.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === '1 agent unhidden');
    await p.getByRole('checkbox',{name:'Pi',exact:true}).check();
    await p.getByRole('button',{name:'Hide selected',exact:true}).click();
    await p.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === '1 agent hidden');
    await p.getByRole('checkbox',{name:'Pi',exact:true}).check();
    await p.getByRole('checkbox',{name:'Zed',exact:true}).check();
    assert.equal(await count.textContent(),'2 agents selected');
  });
  test(`${engine}: an open manager refreshes visibility without losing selection or focus`, async (t) => {
    const f = await fixture(t,engine), p = f.page;
    const search = p.locator('.ag-manage-search');
    await search.fill('Pi');
    const check = p.getByRole('checkbox',{name:'Pi',exact:true});
    await check.check();
    await check.focus();
    const label = p.locator('.ag-manage-row[data-id="Pi"] .ag-manage-state');
    assert.equal(await label.textContent(),'Hidden');
    for (const hidden of [false,true]) {
      f.state.settings.agentsHidden = hidden ? ['Pi'] : [];
      await p.evaluate(() => load());
      assert.equal(await label.textContent(),hidden ? 'Hidden' : 'Not hidden');
      assert(await check.isChecked(),'refresh keeps the selection');
      assert(await check.evaluate((c) => c === document.activeElement),'refresh keeps keyboard focus');
      assert.equal(await search.inputValue(),'Pi','refresh keeps the search');
      assert.equal(await p.getByRole('button',{name:'Hide selected',exact:true}).isDisabled(),hidden);
      assert.equal(await p.getByRole('button',{name:'Unhide selected',exact:true}).isDisabled(),!hidden);
    }
    assert.equal(f.posts.length,0,'refresh never saves the arrangement');
  });
  for (const fail of [false,true]) test(`${engine}: saving blocks Escape and backdrop, then releases on ${fail ? 'failure' : 'success'}`,async (t) => {
    const f = await fixture(t,engine), p = f.page;
    const pending = f.save = {...gate(),fail};
    t.after(() => pending.release.resolve());
    await p.getByRole('checkbox',{name:'Zed',exact:true}).check();
    await p.getByRole('button',{name:'Hide selected',exact:true}).click();
    await pending.started.promise;
    await p.keyboard.press('Escape');
    assert.equal(await p.locator('#modal').evaluate((m) => m.hidden || m.classList.contains('out')),false,'Escape must not dismiss an in-flight save');
    await p.locator('#modal').click({position:{x:2,y:2}});
    assert.equal(await p.locator('#modal').evaluate((m) => m.hidden || m.classList.contains('out')),false,'backdrop must not dismiss an in-flight save');
    assert(await p.getByRole('button',{name:'Done',exact:true}).isDisabled());
    assert.equal(f.posts.length,1);
    pending.release.resolve();
    await p.waitForFunction(() => !document.querySelector('.ag-manage-button').disabled);
    if (fail) {
      assert.equal(await p.locator('.ag-manage-result').textContent(),'Save refused');
      assert(await p.getByRole('checkbox',{name:'Zed',exact:true}).isChecked());
      assert.deepEqual(f.state.settings.agentsHidden,['Pi']);
    } else assert.deepEqual(new Set(f.state.settings.agentsHidden),new Set(['Pi','Zed']));
    await close(p);
  });
  test(`${engine}: a later hide from another window is accepted after bulk unhide`,async(t) => {
    const f = await fixture(t,engine), p=f.page;
    await p.getByRole('checkbox',{name:'Pi',exact:true}).check();
    await p.getByRole('button',{name:'Unhide selected',exact:true}).click();
    await p.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === '1 agent unhidden');
    await close(p);
    f.state.settings.agentsHidden=['Pi'];
    await p.evaluate(() => load()); // the same state refresh used when the window regains focus
    await p.getByRole('button',{name:'Manage agents',exact:true}).click();
    assert.equal(await p.locator('.ag-manage-row[data-id="Pi"] .ag-manage-state').textContent(),'Hidden');
    await p.getByRole('checkbox',{name:'Zed',exact:true}).check();
    await p.getByRole('button',{name:'Hide selected',exact:true}).click();
    await p.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === '1 agent hidden');
    assert.deepEqual(new Set(f.state.settings.agentsHidden),new Set(['Pi','Zed']),'next save preserves the other window’s hide');
  });
  test(`${engine}: an older state read cannot undo a completed bulk save`,async(t) => {
    const f=await fixture(t,engine),p=f.page;
    const pending=f.read=gate();
    t.after(() => pending.release.resolve());
    await p.evaluate(() => { window.visibilityRead = load(); });
    await pending.started.promise;
    await p.getByRole('checkbox',{name:'Pi',exact:true}).check();
    await p.getByRole('button',{name:'Unhide selected',exact:true}).click();
    await p.waitForFunction(() => document.querySelector('.ag-manage-result')?.textContent === '1 agent unhidden');
    pending.release.resolve();
    await p.evaluate(() => window.visibilityRead);
    await close(p);
    await p.getByRole('button',{name:'Manage agents',exact:true}).click();
    assert.equal(await p.locator('.ag-manage-row[data-id="Pi"] .ag-manage-state').textContent(),'Not hidden');
  });
}
