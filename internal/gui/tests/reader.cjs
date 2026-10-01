// The app deliberately undoes unsolicited scrollIntoView (including Playwright's
// automatic scroll). Bring a control into sight with real wheel input first.
async function inView(page, locator) {
  await locator.waitFor({state:"visible"});
  if (await locator.evaluate(e => !e.closest("#view-usage"))) return;
  const view = page.locator("#view-usage");
  const v = await view.boundingBox();
  await page.mouse.move(v.x + v.width / 2, v.y + v.height / 2);
  for (let i = 0; i < 80; i++) {
    const b = await locator.boundingBox();
    if (!b) { await page.waitForTimeout(40); continue; }
    if (b.y >= v.y - 1 && b.y + b.height <= v.y + v.height + 1) {
      await page.waitForTimeout(300);
      return;
    }
    const delta = b.y < v.y - 1 ? -20 : 20;
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
