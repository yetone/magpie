// The app deliberately undoes unsolicited scrollIntoView (including Playwright's
// automatic scroll). Bring a control into sight with real wheel input first.
// Wholly in sight: one cut off by a fraction of a pixel at the view's foot
// was scrolled the rest of the way by Playwright's click itself, before the
// app saw the click, and the page moved 1px under it (860 to 861 on the
// Usage page's Fetch from archive, once the archive switch in its header
// had moved the rows 4px down).
async function inView(page, locator) {
  await locator.waitFor({state:"visible"});
  if (await locator.evaluate(e => !e.closest("#view-usage"))) return;
  const view = page.locator("#view-usage");
  const v = await view.boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const b = await locator.boundingBox();
    if (!b) { await page.waitForTimeout(40); continue; }
    // one taller than the view: its top in sight
    if (b.y >= v.y && (b.y + b.height <= v.y + v.height || (b.height > v.height && b.y < v.y + v.height))) {
      await page.waitForTimeout(300);
      return;
    }
    const delta = b.y < v.y ? -20 : 20;
    await page.mouse.wheel(0, delta);
    await page.waitForTimeout(40);
  }
  throw new Error("control did not come into sight: " + JSON.stringify({view:v,target:await locator.boundingBox(),scroll:await view.evaluate(e=>e.scrollTop)}));
}

async function click(page, locator) {
  await inView(page, locator);
  await locator.click();
}

module.exports = { inView, click };
