// Optional browser acceptance for the embedded UI, using the Go test HTTP fixture.
// Playwright is a development-only dependency, never part of the shipped assets.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("playwright");

async function run() {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1440, height: 1100 } });
    const page = await context.newPage();
    const errors = [];
    const previews = [];
    const baseURL = new URL(process.env.ARPRT_DASHBOARD_URL);
    let requests = 0;
    let mode = "pass";
    let releaseInitial;
    const initialGate = new Promise((resolve) => { releaseInitial = resolve; });
    let initialRequest = true;
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("request", (request) => {
      const url = new URL(request.url());
      assert.equal(url.origin, baseURL.origin, "requests must stay on the dashboard origin");
      assert(url.pathname.startsWith(baseURL.pathname), "requests must stay under the dashboard base path");
      if (request.url().includes("/preview/")) previews.push(request.url());
    });
    await page.clock.install();
    await page.route("**/api/devices?*", async (route) => {
      requests++;
      if (initialRequest) {
        initialRequest = false;
        await initialGate;
      }
      if (mode === "failure") return route.fulfill({ status: 503, contentType: "application/json", body: '{"error":"Service Unavailable"}' });
      if (mode === "expired") return route.fulfill({ status: 410, contentType: "application/json", body: '{"error":"Gone"}' });
      if (mode === "empty") return route.fulfill({ json: { devices: [], observedAt: new Date().toISOString(), pollSeconds: 300 } });
      if (mode === "update") {
        const response = await route.fetch();
        const data = await response.json();
        for (const device of data.devices) {
          device.firmware = '<img src=x onerror="alert(1)">';
          device.battery = { percent: 25 };
        }
        return route.fulfill({ response, json: data });
      }
      return route.continue();
    });
    const rows = page.locator(".device");
    const row = (number) => page.locator(`.device[data-name="device-${String(number).padStart(2, "0")}"]`);
    const ready = () => page.waitForFunction(() => document.getElementById("devices").getAttribute("aria-busy") === "false");
    async function poll() {
      const before = requests;
      await page.clock.fastForward(31000);
      await page.waitForFunction(() => document.getElementById("devices").getAttribute("aria-busy") === "false");
      assert(requests > before, "UI should refresh every 30 seconds independently of device polling");
    }
    async function screenshot(name) {
      const directory = process.env.ARPRT_DASHBOARD_SCREENSHOTS;
      if (!directory) return;
      await fs.mkdir(directory, { recursive: true });
      await page.screenshot({ path: path.join(directory, `${name}.png`) });
    }
    async function noOverflow() {
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), "page must fit viewport");
    }

    await page.goto(process.env.ARPRT_DASHBOARD_URL);
    await page.getByText("Loading devices…", { exact: true }).waitFor();
    releaseInitial();
    await row(20).waitFor();
    await ready();
    await row(1).locator("img").waitFor({ state: "visible" });
    await page.waitForFunction(() => document.querySelector('.device img')?.naturalWidth > 0);
    assert.equal(await rows.count(), 20);
    assert.equal(await page.locator("html").getAttribute("lang"), "en");
    assert.equal(await page.locator("#page-count").textContent(), "20 on this page");
    assert.match(await row(1).innerText(), /3\.90 V/);
    assert.deepEqual(await row(1).locator(".badge").allTextContents(), ["Ready", "Ready", "Ready"]);
    assert.doesNotMatch(await row(1).innerText(), /\d+%/);
    assert.match(await row(2).innerText(), /0% · 3\.90 V/);
    assert.match(await row(2).innerText(), /Serving stale image/);
    assert.match(await row(2).innerText(), /Render failed/);
    assert.match(await row(3).innerText(), /Silent/);
    assert.match(await row(3).innerText(), /Render failed/);
    assert(await row(3).locator(".problems li").count() >= 3, "simultaneous problems must stay visible");
    assert.match(await row(4).innerText(), /Never seen/);
    assert.match(await row(4).innerText(), /No image available/);
    assert.match(await row(5).innerText(), /Image pending/);
    assert.match(await row(5).innerText(), /Playlist/);
    assert.match(await row(6).innerText(), /Automatic selection/);
    assert.match(await row(7).innerText(), /Preview unavailable/);
    assert.match(await row(8).innerText(), /Generated —/);
    assert.match(await row(9).innerText(), /82%/);
    assert(previews.length > 0 && previews.length < 10, "offscreen previews must stay unloaded");
    assert(previews.every((url) => !/device-2[1-3]/.test(url)), "other pages must not load images");
    await noOverflow();
    await screenshot("desktop");

    const enlarge = row(1).getByRole("button", { name: /Enlarge/ });
    await enlarge.focus();
    await page.keyboard.press("Enter");
    await page.locator("dialog[open]").waitFor();
    assert.equal(await page.locator("#close-preview").evaluate((el) => document.activeElement === el), true);
    await page.waitForFunction(() => document.querySelector(".dialog-image img")?.naturalWidth > 0);
    await screenshot("preview");
    await page.keyboard.press("Tab");
    await page.keyboard.press("Tab");
    assert(await page.evaluate(() => document.querySelector("dialog").contains(document.activeElement)), "modal must contain keyboard focus");
    await page.keyboard.press("Escape");
    // The close event clears the preview after the native dialog restores focus.
    await page.waitForFunction(() => document.getElementById("preview-content").childElementCount === 0);
    assert.equal(await enlarge.evaluate((el) => document.activeElement === el), true);
    assert.equal(await page.locator("#preview-content").textContent(), "");

    // API failures keep the successful list and its original update timestamp.
    const originalUpdated = await page.locator("#updated time").getAttribute("datetime");
    mode = "failure";
    await poll();
    assert.match(await page.locator("#notice").innerText(), /Showing the last successful update/);
    assert.equal(await rows.count(), 20);
    assert.equal(await page.locator("#updated time").getAttribute("datetime"), originalUpdated);
    mode = "pass";
    await page.getByRole("button", { name: "Try again", exact: true }).click();
    await page.locator("#notice").waitFor({ state: "hidden" });
    await ready();

    // Quiet polls reuse images. Changed rows retain focus and expanded members.
    await row(5).locator("summary").click();
    const summary = row(5).locator("summary");
    await summary.focus();
    const countsBeforeUpdate = new Map(previews.map((url) => [url, previews.filter((item) => item === url).length]));
    mode = "update";
    await poll();
    assert.equal(await row(5).locator("details").getAttribute("open"), "");
    assert.equal(await summary.evaluate((el) => document.activeElement === el), true);
    for (const [url, count] of countsBeforeUpdate) assert.equal(previews.filter((item) => item === url).length, count, `telemetry refresh should reuse loaded preview: ${url}`);
    assert.match(await row(1).innerText(), /<img src=x onerror="alert\(1\)">/);
    assert.equal(await page.locator('img[src="x"]').count(), 0, "API text must not become HTML");
    assert.match(await row(1).innerText(), /25%/);
    assert.match(await row(5).innerText(), /missing-screen · Unavailable/);
    mode = "pass";

    // Failed navigation and expired cursors preserve the page until recovery.
    mode = "failure";
    await page.getByRole("button", { name: "Next →" }).click();
    await page.locator("#notice").waitFor({ state: "visible" });
    await ready();
    assert.equal(await page.locator("#page-number").textContent(), "Page 1");
    mode = "pass";
    await page.getByRole("button", { name: "Next →" }).click();
    await row(23).waitFor();
    await ready();
    assert.equal(await rows.count(), 3);
    assert.equal(await page.locator("#page-number").textContent(), "Page 2");
    assert.equal(await page.locator("#next").isDisabled(), true);
    assert.equal(await page.locator("#page-title").evaluate((el) => document.activeElement === el), true);
    mode = "expired";
    await poll();
    assert.match(await page.locator("#notice").innerText(), /page has expired/);
    assert.equal(await rows.count(), 3);
    mode = "pass";
    await page.getByRole("button", { name: "First page", exact: true }).click();
    await row(20).waitFor();
    await ready();
    assert.equal(await page.locator("#page-number").textContent(), "Page 1");

    for (const width of [768, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.evaluate(() => scrollTo(0, 0));
      await noOverflow();
      if (width === 390) await screenshot("mobile");
    }
    await row(1).getByRole("button", { name: /Enlarge/ }).focus();
    await page.keyboard.press("Enter");
    await page.locator("dialog[open]").waitFor();
    await noOverflow();
    await screenshot("mobile-preview");
    await page.getByRole("button", { name: /Close/ }).click();
    await row(5).locator("summary").scrollIntoViewIfNeeded();
    await row(5).locator("summary").focus();
    await page.keyboard.press("Enter");
    assert.equal(await row(5).locator("details").getAttribute("open"), "");

    // A preview may disappear between listing and image fetch; retry recovers.
    let failPreview = true;
    await page.route("**/api/devices/device-01/preview/*", (route) => failPreview ? route.fulfill({ status: 404 }) : route.continue());
    await page.reload();
    await row(1).scrollIntoViewIfNeeded();
    await row(1).getByRole("button", { name: "Retry preview" }).waitFor();
    failPreview = false;
    await row(1).getByRole("button", { name: "Retry preview" }).click();
    await page.waitForFunction(() => document.querySelector('.device img')?.naturalWidth > 0);

    mode = "empty";
    await poll();
    await page.getByText("No devices found", { exact: true }).waitFor();
    assert.equal(await rows.count(), 0);
    assert.equal(await page.locator("#pagination").isVisible(), false);
    await screenshot("empty");
    mode = "failure";
    await page.reload();
    await page.getByText("Could not load devices. Check the connection and try again.", { exact: true }).waitFor();
    assert.equal(await page.locator("#loading").isVisible(), false);
    mode = "pass";
    await page.getByRole("button", { name: "Try again", exact: true }).click();
    await row(20).waitFor();
    await ready();
    assert.deepEqual(errors, [], "browser should have no JavaScript errors");
    console.log("Dashboard browser checks passed: API/PVC previews, states, telemetry, lazy images, polling, recovery, pagination, keyboard, responsive layout and safe text.");
  } finally {
    await browser.close();
  }
}

run().catch((error) => { console.error(error); process.exitCode = 1; });
