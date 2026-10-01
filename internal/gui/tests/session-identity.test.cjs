// Provider attribution requires route evidence. Models and session identities
// never promote unknown requests to official providers. Legacy model_vendor
// hints in fixtures are deliberately ignored. The API is a fixture.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = path.resolve(__dirname, "../assets");
const now = new Date().toISOString();
const rows = [
  { t:now, agent:"claude-desktop", agentName:"Claude Desktop", provider:"session-anthropic", providerName:"Anthropic", session_account:"historical@example.com", session_official_login:true, model:"claude-opus-5", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-openai", providerName:"OpenAI", session_account:"recorded@example.com", session_official_login:true, session_provider:"openai", model:"gpt-6-sol", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", model:"gpt-6-astra", model_vendor:"OpenAI", session_provider:"relay", session_account:"creator@example.com", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"claude-opus-5", req:"claude-opus-5[1m]", model_vendor:"Anthropic", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"relay", providerName:"My Relay", host:"relay.example", access:"api", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
  { t:now, agent:"claude", agentName:"Claude Code", provider:"session-unknown", providerName:"Local session", model:"unknown-alias", source:"log", in:10, out:2, status:0, cost:0, priced:false },
  { t:now, agent:"opencode", agentName:"OpenCode", provider:"unknown-relay", providerName:"Archived relay", model:"gpt-6-astra", in:10, out:2, status:200, cost:0, priced:false },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", session_provider:"custom", session_account:"reviewer@example.com", session_official_login:true, req:"codex-auto-review", model:"codex-auto-review", source:"log", in:10, out:2, status:0, cost:0.003, priced:true, pricing_model:"gpt-5.6-luna" },
  { t:now, agent:"codex", agentName:"Codex", provider:"session-unknown", providerName:"Local session", session_provider:"my-custom-route", model:"gpt-6-astra", source:"log", in:10, out:2, status:0, cost:0, priced:false },
];

[10000,10001,30000,30001,1250,0,undefined,900,-1].forEach((ms,i) => rows[i].ms=ms);
rows.push({t:now,agent:"codex",agentName:"Codex",provider:"codex",providerName:"ChatGPT",access:"subscription",model:"gpt-6-sol",in:10,out:2,status:200,ms:4000,cost:0,priced:false});

for (const engine of ["chromium", "webkit"]) {
  test(engine + ": session account identities", async t => {
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({channel:"chromium"}));
    t.after(() => browser.close());
    for (const lang of ["en", "zh"]) for (const theme of ["light", "dark"]) {
      await t.test(lang + " / " + theme, async () => {
        const page = await browser.newPage({viewport:{width:1200,height:900},reducedMotion:"reduce"});
        const errors = [];
        page.on("pageerror", e => errors.push(e.message));
        await page.route("**/*", async route => {
          const url = new URL(route.request().url());
          const json = data => route.fulfill({json:data});
          if (url.pathname === "/boot.js") return route.fulfill({contentType:"text/javascript",body:`window.bootPrefs={lang:"${lang}",theme:"${theme}",web:false};`});
          if (url.pathname === "/wails/runtime.js") return route.fulfill({contentType:"text/javascript",body:"export const Window={};"});
          if (url.pathname === "/api/state") return json({agents:[],profiles:[],settings:{lang,theme}});
          if (url.pathname === "/api/usage/requests") return json({period:"30d",rows,calls:rows.length,total:rows.length,offset:0,errors:0,input:100,output:20,cache_read:0,cache_write:0,cost:0,unpriced:rows.length,series:[],by:{},agents:[],providers:[]});
          if (url.pathname === "/api/usage/quotas") return json([]);
          if (url.pathname === "/api/usage") return json({calls:0,cost:0,series:[],agents:[],models:[]});
          if (url.pathname === "/api/sessions") return json({sessions:[],dirs:[]});
          if (url.pathname === "/api/sessions/stats") return json({days:[],agents:{}});
          if (url.pathname === "/api/groups") return json({groups:[],models:[]});
          if (url.pathname.startsWith("/api/")) return json({});
          const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
          await route.fulfill({body:await fs.readFile(file),contentType:{".html":"text/html",".css":"text/css",".js":"text/javascript",".svg":"image/svg+xml"}[path.extname(file)]});
        });
        await page.goto("http://magpie.test/");
        await page.locator('[data-view="usage"]').first().click();
        await page.locator("#usageTab .opt").nth(1).click();
        await page.locator(".led-row").first().waitFor();
        const cells = page.locator(".led-row .where");
        const localName = lang === "zh" ? "本地会话" : "Local session";
        for (const [i, account] of [[0,"historical@example.com"],[1,"recorded@example.com"],[2,"creator@example.com"],[7,"reviewer@example.com"]]) {
          const text = await cells.nth(i).textContent();
          const official = [0,1,7].includes(i);
          assert.equal(await cells.nth(i).locator(".where-name").textContent(),account);
          assert.equal(await cells.nth(i).locator(".official").count(),official ? 1 : 0);
          if (official) assert.equal(await cells.nth(i).locator(".official").textContent(),lang === "zh" ? "官方" : "OFFICIAL");
          assert(text.includes(localName));
          assert.equal(await cells.nth(i).locator(".access").count(),0,"account login does not establish a supplier route");
          for (const inferred of ["OpenAI","Anthropic","custom","relay","官方登录","ChatGPT login"]) assert(!text.includes(inferred));
        }
        for (const i of [3,5,8]) {
          assert.equal(await cells.nth(i).textContent(),localName,"no account means only Local session, regardless of model/provider ID");
        }
        assert((await cells.nth(4).textContent()).includes("My Relay · relay.example"));
        assert.equal(await cells.nth(4).locator(".access").textContent(),"API");
        assert((await cells.nth(6).textContent()).startsWith("Archived relay"));
        assert.equal(await cells.nth(6).locator(".src").count(),0,"an unknown gateway provider is not a local session");
        for (const [i,band] of [[0,"fast"],[1,"slow"],[2,"slow"],[3,"long"],[4,"fast"],[7,"fast"]]) {
          const cell=page.locator(".led-row .duration").nth(i);
          assert((await cell.getAttribute("class")).includes("duration-"+band));
          assert((await cell.getAttribute("title")).includes(lang === "zh" ? "耗时颜色" : "Duration colors"));
          if (rows[i].source === "log") assert((await cell.textContent()).startsWith("≈"),"color must preserve estimated timing");
        }
        for (const i of [5,6,8]) assert.equal(await page.locator(".led-row .duration").nth(i).textContent(),"—");
        const palette=await page.evaluate(() => {
          const selectors=[".src.official",".src.local",".access-api",".access-subscription"];
          const canvas=document.createElement("canvas"),ctx=canvas.getContext("2d");canvas.width=canvas.height=1;
          const rgb=color=>{ctx.clearRect(0,0,1,1);ctx.fillStyle=color;ctx.fillRect(0,0,1,1);return [...ctx.getImageData(0,0,1,1).data].slice(0,3);};
          const luminance=c=>c.map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((a,v,i)=>a+v*[.2126,.7152,.0722][i],0);
          return selectors.map(selector=>{const s=getComputedStyle(document.querySelector(selector)),a=luminance(rgb(s.color)),b=luminance(rgb(s.backgroundColor));return {color:s.color,contrast:(Math.max(a,b)+.05)/(Math.min(a,b)+.05)};});
        });
        assert.equal(new Set(palette.map(p=>p.color)).size,4,"source types have distinct colors");
        assert(palette.every(p=>p.contrast>=4.5),"small badge text remains readable: "+JSON.stringify(palette));
        if (process.env.MAGPIE_STYLE_SHOTS && lang === "zh" && engine === "chromium") {
          await page.setViewportSize({width:1900,height:950});
          await page.locator(".led-wrap").screenshot({path:process.env.MAGPIE_STYLE_SHOTS+"/badges-"+theme+".png"});
          await page.setViewportSize({width:1200,height:900});
        }
        await page.locator(".led-row").nth(2).click();
        const detail = page.locator(".led-detail");
        assert((await detail.textContent()).includes("relay"),"raw provider ID remains available in details");
        assert((await detail.textContent()).includes("creator@example.com"));
        assert((await detail.textContent()).includes(lang === "zh" ? "不推断供应商" : "no service provider is inferred"));
        const review = page.locator(".led-row").nth(7);
        assert.equal(await review.locator("td").nth(5).textContent(),"—","an estimate must not claim a served model");
        assert.equal(await review.locator(".swap").count(),0,"price alias is not observed forwarding");
        assert((await review.locator(".price-reference").textContent()).includes("gpt-5.6-luna"));
        for (const width of [1200,440]) {
          await page.setViewportSize({width,height:900});
          const layout = await cells.nth(7).evaluate(cell => {
            const badges = [...cell.querySelectorAll(".source-badges .src")].map(n => n.getBoundingClientRect());
            const bounds = cell.getBoundingClientRect();
            const name = cell.querySelector(".where-name").getBoundingClientRect();
            return badges.length === 2 && Math.abs(badges[0].top - badges[1].top) < 1 && badges[0].right < badges[1].left && badges.every(b => b.top >= name.bottom && b.left >= bounds.left && b.right <= bounds.right && b.bottom <= bounds.bottom);
          });
          assert(layout,"both badges share one row below the account at " + width);
        }
        await page.setViewportSize({width:1200,height:900});
        await review.focus();
        await page.keyboard.press("Enter");
        const officialDetail = page.locator(".led-detail").last();
        assert((await officialDetail.textContent()).includes(lang === "zh" ? "官方登录" : "Official login"));
        assert((await cells.nth(7).locator(".official").getAttribute("title")).includes(lang === "zh" ? "不代表已确认这次请求" : "does not establish the route"));
        assert.deepEqual(errors,[]);
        await page.close();
      });
    }
  });
}
