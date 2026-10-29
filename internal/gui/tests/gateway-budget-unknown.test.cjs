const assert = require("node:assert/strict");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { fixture } = require("./fixtures/caller-keys.cjs");

for (const engine of ["chromium", "webkit"]) {
  for (const lang of ["en", "zh", "zh-TW", "ja", "de"]) {
    test(`${engine} ${lang}: unknown budget never appears as zero usage`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const page = await browser.newPage({ viewport: { width: 1100, height: 760 }, reducedMotion: "reduce" });
      const error = "gateway key usage is unavailable: usage ledger is incomplete";
      await page.route("**/*", fixture(lang, "light", [], { lan: true, limits: {
        laptop: { limit: { period: "day", tokens: 1000000 }, used: { period: "day", tokenLimit: 1000000, tokens: 0, tokensLeft: 1000000, error } },
      } }));
      await page.goto("http://magpie.test/?view=gateway");
      const row = page.locator('#gatewayKeys .acc[data-key="laptop"]');
      await row.locator(".key-limit").waitFor();
      assert.equal(await row.locator(".key-limit").textContent(), error);
      assert.equal(await row.locator(".amodels.limit").getAttribute("title"), error);
      assert.doesNotMatch(await row.locator(".amodels.limit").textContent(), /0\s*\//);
      await row.locator(".amodels.limit").click();
      await row.locator(".key-limit-ed").waitFor();
      assert.match(await row.locator(".key-limit-ed").textContent(), /usage is unavailable/);
    });
  }
}
