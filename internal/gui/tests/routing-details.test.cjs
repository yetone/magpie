// Request details are optional: the closed summary stays steady as prompt
// contents change, with errors still visible and full details a click away.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { test } = require('node:test');
const { chromium, webkit } = require('playwright');
const assets = process.env.MAGPIE_DETAIL_ASSETS || path.resolve(__dirname, '../assets');
const now = new Date().toISOString();
const account = { id: 'codex', provider: 'codex', name: 'Codex', who: 'demo@example.com', icon: 'codex-color', kind: 'account', model: 'gpt-6-luna' };
const prompt = { window: 272000, tokens: 43900, counted: true, turns: 1,
  parts: [{ kind: 'system', tokens: 10000, items: [{ name: 'prompt', tokens: 10000 }] },
    { kind: 'tools', tokens: 9500, items: [{ name: 'exec_command', tokens: 9500 }] },
    { kind: 'memory', tokens: 5300, items: [{ name: '/demo/AGENTS.md', tag: 'project', tokens: 5300 }] },
    { kind: 'results', tokens: 14500, items: [{ name: 'exec_command', tag: 'result', n: 4, tokens: 14500 }] },
    { kind: 'chat', tokens: 4600, items: [{ name: '1', tag: 'turn', tokens: 4600 }] }] };
function request(id, extra = {}) {
  return { id, seq: id, time: now, agent: 'codex', model: 'codex/gpt-6-luna', provider: 'codex',
    kind: 'ambient_suggestions', order: [account], tries: [{ id: account.id, model: account.model, effort: 'medium', start: now, done: true, status: 200, ms: 5900, ttft: 5700 }],
    done: true, status: 200, ms: 5900, ttft: 5700, tokens: 44100, out: 200, prompt,
    usage: [{ in: 3200, cache_read: 40700, out: 200 }], ...extra };
}
function serve(lang, feed) {
  return async (r) => {
    const url = new URL(r.request().url()), json = (data) => r.fulfill({ json: data });
    if (url.pathname === '/boot.js') return r.fulfill({ contentType: 'text/javascript', body: `window.bootPrefs={lang:"${lang}",theme:"dark",web:true};` });
    if (url.pathname === '/wails/runtime.js') return r.fulfill({ contentType: 'text/javascript', body: 'export const Window={};' });
    if (url.pathname === '/api/state') return json({ agents: [{ id: 'codex', name: 'Codex', fields: [] }], profiles: [], settings: { lang, theme: 'dark' } });
    if (url.pathname === '/api/gateway/trace') {
      if (!url.searchParams.has('wait')) feed.count = 1;
      const routes = url.searchParams.has('wait') ? await new Promise((resolve) => { feed.next = resolve; }) : [request(100)];
      return json({ mine: true, now, seq: routes[0]?.id || 100, totals: { requests: 1, rerouted: 0, errors: 0 }, routes });
    }
    if (url.pathname === '/api/gateway/history') return json({ days: [], routes: [] });
    if (url.pathname === '/api/providers') return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === '/api/groups') return json({ groups: [] });
    if (url.pathname.startsWith('/api/')) return json({});
    const file = path.join(assets, url.pathname === '/' ? 'index.html' : url.pathname);
    await r.fulfill({ body: await fs.readFile(file), contentType: { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png' }[path.extname(file)] });
  };
}
for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit']) {
  for (const lang of ['en', 'zh', 'zh-TW', 'ja', 'de']) {
    for (const width of [2048, 1440, 1354, 1280, 900, 880, 860, 420, 360]) {
      test(`${engine} ${lang} ${width}px: optional routing details stay compact and retain their preferences`, async (t) => {
        const browser = await (engine === 'webkit' ? webkit.launch() : chromium.launch({ channel: 'chromium' }));
        t.after(() => browser.close());
        const page = await browser.newPage({ viewport: { width, height: 1250 }, reducedMotion: 'reduce' });
        page.setDefaultTimeout(5000);
        const feed = {}, errors = [];
        page.on('pageerror', (e) => errors.push(e.message));
        await page.route('**/*', serve(lang, feed));
        await page.goto('http://magpie.test/?view=routing');
        const toggle = page.locator('.rt-ctx-toggle');
        await page.locator('.rt-ctx').waitFor();
        assert.equal(await page.locator('.rt-ctx .ctx-waffle').count(), 0, 'closed context does not build the detail grid');
        assert.equal(await toggle.getAttribute('aria-expanded'), 'false', 'context starts closed');
        assert.match(await toggle.innerText(), /43\.9K.*272K.*16%.*93%/s);
        assert.equal(await page.locator('.rt-mode').isVisible(), false, 'policy prose is optional');
        assert.equal(await page.locator('.rt-mode-toggle').count(), 0, 'policy shares the request disclosure instead of a second button');
        assert.equal(await page.locator('.rt-steps li.ok').count(), 0, 'routine success prose is already in the result summary');
        assert.match(await page.locator('.rt-brief').innerText(), /demo@example.com/);
        assert.ok(await page.locator('.rt-brief').evaluate((b) => {
          const metrics = b.querySelector('.rt-brief-metrics').getBoundingClientRect(), path = b.querySelector('.rt-brief-path').getBoundingClientRect();
          return metrics.bottom <= path.top;
        }), 'request performance appears before its model and account');
        const accountLayout = await page.locator('.rt-brief-path .rt-named').evaluate((a) => {
          const icon = a.querySelector('.ic').getBoundingClientRect(), name = a.querySelector('.rt-account-name').getBoundingClientRect();
          return { gap: name.left - icon.right, centers: Math.abs(icon.y + icon.height / 2 - name.y - name.height / 2) };
        });
        assert.ok(accountLayout.gap >= 5 && accountLayout.centers < 1, 'account icon has space and is vertically centred with its name');
        assert.equal(await page.locator('.rt-steps li.why').isVisible(), false, 'account selection reasons are hidden while closed');
        const disclosure = page.locator('.rt-detail-toggle');
        assert.ok(await disclosure.evaluate((b) => {
          const r = b.getBoundingClientRect(), p = b.parentElement.getBoundingClientRect();
          return Math.abs(r.x - p.x) < 1 && Math.abs(r.width - p.width) < 1 && r.height >= 32;
        }), 'the entire request row is an easy-to-click disclosure');
        const preview = await page.locator('.rt-brief-path').evaluate((s) => {
          const style = getComputedStyle(s);
          return { height: s.clientHeight, opacity: Number(style.opacity), filter: style.filter, mask: style.maskImage, hidden: s.getAttribute('aria-hidden') };
        });
        assert.ok(preview.height > 0 && preview.height <= 32 && preview.mask !== 'none', 'the closed preview starts at the request path');
        assert.equal(await page.locator('.rt-brief-metrics').evaluate((m) => getComputedStyle(m).maskImage), 'none', 'request metrics remain fully visible');
        assert.equal(preview.opacity, 1, 'the preview uses a gradual fade');
        assert.equal(preview.filter, 'none', 'the preview is not blurred');
        assert.equal(preview.hidden, 'true', 'the closed preview is not read as full details');
        assert.equal(await disclosure.locator(':scope > svg').count(), 0, 'the request has no separate arrow below it');
        assert.ok(await disclosure.getAttribute('aria-label'), 'the row has an accessible action name');
        assert.ok(await page.locator('.rt-detail-hint').isVisible(), 'the inline action is discoverable without hovering');
        const closedHint = await page.locator('.rt-detail-hint').innerText();
        assert.equal(await disclosure.getAttribute('aria-label'), closedHint, 'the visible and accessible actions have the same name');
        // Card widths are applied by ResizeObserver on the next frame.
        // Wait for the visible responsive layout before measuring updates.
        const cardWidth = await page.locator('.rt').evaluate((c) => c.clientWidth - parseFloat(getComputedStyle(c).paddingLeft) - parseFloat(getComputedStyle(c).paddingRight));
        await page.waitForFunction(({ w, cardWidth }) => {
          const stage = document.querySelector('.rt-stage'), story = document.querySelector('.rt-story');
          const a = stage.getBoundingClientRect(), b = story.getBoundingClientRect();
          const columns = getComputedStyle(stage).gridTemplateColumns.trim().split(/\s+/).length;
          return columns === (w <= 420 ? 2 : 3) && (cardWidth >= 1280 ? b.left >= a.right : b.top >= a.bottom);
        }, { w: width, cardWidth });
        // Read geometry together: a WebKit layout/scroll anchoring frame
        // between separate boundingBox calls gives different coordinates.
        const { story, context, stage } = await page.evaluate(() => {
          const rect = (s) => { const b = document.querySelector(s).getBoundingClientRect(); return { x: b.x, y: b.y, width: b.width, height: b.height }; };
          return { story: rect('.rt-story'), context: rect('.rt-ctx'), stage: rect('.rt-stage') };
        });
        assert.ok(context.y >= story.y + story.height, 'context sits below the request, avoiding a tall inspector beside an empty column');
        if (cardWidth >= 1280) assert.ok(story.x >= stage.x + stage.width, 'wide request story sits beside the diagram');
        else assert.ok(story.y >= stage.y + stage.height, 'narrow request story sits below the diagram');
        const send = async (r) => {
          for (let i = 0; i < 100 && !feed.next; i++) await page.waitForTimeout(20);
          assert.ok(feed.next, 'the trace poll is waiting');
          const answer = feed.next; feed.next = null; answer([r]);
          await page.waitForFunction((n) => document.querySelectorAll('.rt-req').length === n, ++feed.count);
        };
        const before = await page.locator('#rtMore').boundingBox();
        for (let id = 101; id <= 103; id++) {
          const parts = [...prompt.parts, { kind: 'files', tokens: 100, items: Array.from({ length: id }, (_, i) => ({ name: `/demo/file-${i}.go`, tokens: 1 })) }];
          await send(request(id, { prompt: { ...prompt, parts } }));
          assert.equal(await page.locator('.rt-ctx .ctx-waffle').count(), 0);
          assert.ok(Math.abs((await page.locator('#rtMore').boundingBox()).y - before.y) < 2, 'changing prompt contents must not move the request list');
        }
        const scroll = () => page.locator('#view-routing').evaluate((v) => v.scrollTop);
        const top = await scroll();
        await toggle.click();
        assert.equal(await scroll(), top, 'opening context does not scroll');
        await page.locator('.rt-ctx .ctx-waffle').waitFor();
        assert.equal(await page.locator('.rt-ctx .ctx-title').count(), 1, 'there is one clickable context header');
        assert.equal(await toggle.locator('.ctx-summary-used, .ctx-health, .ctx-summary-cache').count(), 0, 'expanded header does not repeat the detail counts');
        assert.equal(await page.locator('.rt-ctx .ctx-waffle > i').count(), 400);
        assert.ok((await page.locator('.rt-ctx .ctx-waffle').boundingBox()).height <= 80, 'the grid height does not grow with the window width');
        const overview = await page.locator('.ctx-overview').boundingBox(), contents = await page.locator('.rt-ctx .ctx-contents').boundingBox();
        if (cardWidth >= 800) {
          assert.ok(contents.x > overview.x + overview.width, 'wide context pairs its overview with contents');
          assert.ok(Math.abs(contents.height - overview.height) < 2, 'neither column leaves a tall empty inspector beside routing');
        } else assert.ok(contents.y >= overview.y + overview.height, 'narrow context stacks its contents');
        if (cardWidth >= 1280) assert.ok(await page.evaluate(() => {
          const upper = document.querySelector('.rt-log').getBoundingClientRect(), lower = document.querySelector('.rt-ctx .ctx-contents').getBoundingClientRect();
          return Math.abs(upper.x - lower.x) < 1;
        }), 'upper and lower column separators align');
        const restingBackground = await toggle.evaluate((b) => getComputedStyle(b).backgroundColor);
        await toggle.hover();
        assert.equal(await toggle.evaluate((b) => getComputedStyle(b).backgroundColor), restingBackground, 'header hover does not paint a full-width strip');
        const head = await page.locator('.rt-ctx .ctx-contents-head').evaluate((h) => {
          const label = h.querySelector('.ctx-k').getBoundingClientRect(), filters = h.querySelector('.segs').getBoundingClientRect();
          return { label: label.y + label.height / 2, filters: filters.y + filters.height / 2, fits: h.scrollWidth <= h.clientWidth + 1 };
        });
        assert.ok(Math.abs(head.label - head.filters) < 1 && head.fits, 'contents label and filters share a line at every width');
        const detailTop = await scroll();
        const row = await disclosure.boundingBox();
        await page.mouse.click(row.x + 4, row.y + row.height / 2);
        assert.equal(await scroll(), detailTop, 'opening routing details does not scroll');
        assert.equal(await disclosure.getAttribute('aria-expanded'), 'true', 'the far left of the row opens the details');
        assert.notEqual(await page.locator('.rt-detail-hint').innerText(), closedHint, 'the inline action changes to collapse');
        assert.equal(await page.locator('.rt-detail-preview').getAttribute('aria-hidden'), 'false');
        assert.equal(await page.locator('.rt-brief-path').evaluate((s) => getComputedStyle(s).maskImage), 'none', 'the opened path and explanations are fully readable');
        assert.equal(await page.locator('.rt-steps li.why').isVisible(), true);
        assert.ok(await page.locator('.rt-story').evaluate((s) => {
          const dot = s.querySelector('.rt-brief-state').getBoundingClientRect(), center = dot.x + dot.width / 2;
          return [...s.querySelectorAll('.rt-steps li')].every((li) => {
            const marker = getComputedStyle(li, '::before');
            return Math.abs(li.getBoundingClientRect().x + parseFloat(marker.left) + parseFloat(marker.width) / 2 - center) < 1;
          });
        }), 'result and explanation dots share the same left column');
        assert.equal(await page.locator('.rt-steps li.kind').isVisible(), true, 'the unique purpose explanation is still available');
        assert.equal(await page.locator('.rt-steps li.kind .kind').count(), 0, 'purpose explanation does not repeat the badge');
        assert.equal(await page.locator('.rt-policy').isVisible(), true, 'the same control reveals policy');
        assert.equal(await page.locator('.rt-policy-title').innerText(), { en: 'Current routing policy', zh: '当前路由策略说明', 'zh-TW': '目前路由策略說明', ja: '現在のルーティング方針', de: 'Aktuelle Routing-Strategie' }[lang]);
        assert.equal(await page.locator('.rt').evaluate((c) => c.clientWidth - parseFloat(getComputedStyle(c).paddingLeft) - parseFloat(getComputedStyle(c).paddingRight)), cardWidth, 'opening details keeps the available width steady');
        assert.ok(await page.locator('.rt-path-row').evaluate((r) => {
          const b = r.getBoundingClientRect(), m = r.querySelector('.rt-detail-hint').getBoundingClientRect();
          return Math.abs(m.right - b.right) < 1 && m.y >= b.y && m.bottom <= b.bottom;
        }), 'the action label stays inside the request row at its right edge');
        assert.ok(await page.locator('.rt-brief-effort').evaluate((e) => {
          const r = e.getBoundingClientRect();
          return e.contains(document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2));
        }), 'expanded effort hints remain reachable under the whole-row control');
        // Enter establishes keyboard focus before Space acts on the button.
        await disclosure.press('Enter');
        assert.equal(await disclosure.getAttribute('aria-expanded'), 'false', 'Enter toggles the disclosure');
        await page.keyboard.press('Space');
        assert.equal(await disclosure.getAttribute('aria-expanded'), 'true', 'Space toggles the focused disclosure');
        const openedRow = await disclosure.boundingBox();
        await page.mouse.click(openedRow.x + openedRow.width - 4, openedRow.y + openedRow.height / 2);
        assert.equal(await disclosure.getAttribute('aria-expanded'), 'false', 'the far right of the row closes the details');
        const closedRow = await disclosure.boundingBox();
        await page.mouse.click(closedRow.x + closedRow.width - 4, closedRow.y + closedRow.height / 2);
        assert.equal(await disclosure.getAttribute('aria-expanded'), 'true', 'the far right of the row reopens the details');
        assert.equal(await scroll(), detailTop, 'the stationary row does not scroll when reopened');
        assert.ok(await page.locator('.rt-details').evaluate((d) => {
          const why = d.querySelector('li.why').getBoundingClientRect(), purpose = d.querySelector('li.kind').getBoundingClientRect(), steps = d.querySelector('.rt-steps').getBoundingClientRect(), policy = d.querySelector('.rt-policy').getBoundingClientRect();
          return why.bottom <= purpose.top && policy.top >= steps.bottom;
        }), 'specific decision precedes purpose, with general policy last');
        assert.ok((await page.locator('.rt-cap').boundingBox()).width <= 1, 'the live caption does not repeat the decision visually');
        assert.equal((await page.locator('.rt-story').innerText()).split(account.who).length - 1, 1, 'the routine request names its selected account once');
        assert.equal(await page.locator('.rt-brief-path code').count(), 1, 'the provider-prefixed model alias is not repeated');
        assert.equal((await page.locator('.rt-story').innerText()).split(account.model).length - 1, 1, 'the routine request names its model once');
        await send(request(104, { ms: 7100 }));
        assert.match(await page.locator('.rt-brief .duration .v').innerText(), /7\.1/, 'new requests still update the opened story; pinning is a separate change');
        await page.reload();
        await page.locator('.rt-ctx .ctx-waffle').waitFor();
        assert.equal(await toggle.getAttribute('aria-expanded'), 'true', 'reload keeps context open');
        assert.equal(await page.locator('.rt-detail-toggle').getAttribute('aria-expanded'), 'true');
        assert.equal(await page.locator('.rt-policy').isVisible(), true);
        // Expanded details can put the context control below the fold.
        // Scroll as a reader would; programmatic scroll is guarded by the app.
        await page.mouse.move(10, 600);
        for (let i = 0; i < 30 && await toggle.evaluate((b) => b.getBoundingClientRect().bottom > innerHeight - 70); i++) {
          await page.mouse.wheel(0, 150);
          await page.waitForTimeout(80);
        }
        await toggle.click();
        await page.locator('.rt-detail-toggle').click();
        assert.equal(await page.locator('.rt-policy').isVisible(), false, 'closing details also closes policy');
        // A vendor failure remains readable without expanding the story.
        await send(request(110, { status: 429, tries: [{ id: account.id, model: account.model, start: now, done: true, status: 429, fail: 'rate', error: 'Too many requests' }] }));
        await page.locator('.rt-steps li.bad').waitFor();
        assert.equal(await page.locator('.rt-steps li.said').isVisible(), true);
        assert.equal(await page.locator('.rt-brief-path').evaluate((s) => getComputedStyle(s).maskImage), 'none', 'failures are never faded');
        assert.equal(await page.locator('.rt-detail-preview').getAttribute('aria-hidden'), 'false');
        await send(request(111, { prompt: null }));
        await page.waitForFunction(() => document.querySelector('.rt-ctx').hidden);
        await send(request(112, { prompt: { ...prompt, counted: false, window: 0 }, done: false }));
        await toggle.waitFor();
        assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
        assert.doesNotMatch(await toggle.innerText(), /93%|272K/);
        assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'no horizontal page overflow');
        assert.ok(await toggle.evaluate((b) => b.scrollWidth <= b.clientWidth + 1), 'the context summary fits');
        assert.equal(await page.locator('#status.err').count(), 0, 'the fixture leaves no API error over the controls');
        assert.deepEqual(errors, []);
      });
    }
  }
}
