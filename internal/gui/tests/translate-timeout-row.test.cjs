// Run with Node's test runner and Playwright on the module path; see README.md.
// A translation's wait — how long a model is given to translate a request's
// texts and a reply's texts back — is a row of its own: Settings › Models ›
// Translation, between the Replies and the Routing sections. It says what the
// wait is, offers the ones a model may need, and the page sends it with the
// rest of its choices. In every language. No backend, the API is faked here.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const { watchSettingsReads, settingsDrawn } = require("./reader.cjs");

const assets = path.resolve(__dirname, "../assets");

const words = {
  en: {
    unit: "30 s",
    head: "Translation",
    name: "Translation timeout",
    sub: "How long a model is given to translate a request's texts and a reply's texts back.",
  },
  zh: {
    unit: "30 秒",
    head: "翻译",
    name: "翻译超时",
    sub: "模型翻译请求的文字、再把回复的文字翻回去，各自有多长时间。",
  },
  "zh-TW": {
    unit: "30 秒",
    head: "翻譯",
    name: "翻譯逾時",
    sub: "模型翻譯請求的文字、再把回覆的文字翻回去，各自有多長時間。",
  },
  ja: {
    unit: "30 秒",
    head: "翻訳",
    name: "翻訳のタイムアウト",
    sub: "リクエストのテキストを翻訳し、返信のテキストを戻すまでにモデルが与非えられる時間。",
  },
  de: {
    unit: "30 s",
    head: "Übersetzung",
    name: "Zeitlimit für die Übersetzung",
    sub: "Wie lange ein Modell Zeit hat, die Texte einer Anfrage zu übersetzen und die Texte einer Antwort zurück zu übersetzen.",
  },
};

// serve fakes the API: the settings it reads, and the saves the page makes.
function serve(lang, onSave) {
  const settings = { theme: "light", lang, currency: "usd" };
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (body) => route.fulfill({ json: body });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings });
    if (url.pathname === "/api/settings") {
      if (route.request().method() !== "GET") {
        onSave(route.request().postDataJSON());
        return json(settings);
      }
      return json(settings);
    }
    if (url.pathname === "/api/providers") return json({ providers: [], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/usage/quotas") return json([]);
    if (url.pathname === "/api/usage/quotas/history") return json([]);
    if (url.pathname === "/api/usage") return json({ days: [] });
    if (url.pathname === "/api/groups") return json({ groups: [], models: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { await route.fulfill({ status: 404, body: "" }); }
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  test(`${engine}: a translation's wait is a row of its own, in every language`, async (t) => {
    assert(["chromium", "webkit"].includes(engine), "BROWSER must be chromium or webkit");
    const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
    t.after(() => browser.close());
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      let posted = null;
      const saved = new Promise((resolve) => { posted = resolve; });
      const page = await (await browser.newContext({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" })).newPage();
      page.setDefaultTimeout(5000);
      page.setDefaultNavigationTimeout(60000);
      const errors = [];
      page.on("pageerror", (e) => errors.push(e.message));
      await page.route("**/*", serve(lang, (body) => posted(body)));
      await watchSettingsReads(page);
      await page.goto("http://127.0.0.1:4999/?view=settings&tab=models");
      await settingsDrawn(page);

      // the section's own head, between the Replies and the Routing ones
      const heads = await page.locator("#setPage-models .row-head .label").allTextContents();
      assert.ok(heads.includes(w.head), `${lang}: no ${w.head} section: ${heads.join(", ")}`);
      assert.deepEqual(errors, [], `${lang}: the page drew with errors`);

      const row = page.locator("#translateTimeoutRow");
      await row.waitFor();
      assert.equal((await row.locator(".name").textContent()).trim(), w.name, lang);
      assert.ok((await row.locator(".sub").textContent()).startsWith(w.sub), `${lang}: ${await row.locator(".sub").textContent()}`);
      // 30 seconds is the wait unless it is set
      assert.equal((await row.locator(".opt.on").textContent()).trim(), w.unit, lang);

      await row.locator('.opt[data-seg="60"]').click();
      const body = await saved;
      assert.equal(body.translateTimeout, 60, `${lang}: the page sent ${JSON.stringify(body.translateTimeout)}`);
      assert.deepEqual(errors, [], `${lang}: the page drew with errors`);
      await page.context().close();
    }
  });
}
