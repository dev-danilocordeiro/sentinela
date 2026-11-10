// Uso: PWCORE=<caminho do playwright-core> CHROME=<binário do chromium> node scripts/guia-conferir.js "$PWD" docs/guia/capturas
// Confere o guia no Chromium headless: 390 e 1280 px, temas claro e escuro.
const { chromium } = require(process.env.PWCORE);
const path = require("path");
const root = process.argv[2], shots = process.argv[3];
const url = "file://" + path.join(root, "docs/guia/index.html");
(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME });
  let failures = 0;
  const fail = (m) => { failures++; console.log("FALHA: " + m); };
  for (const width of [390, 1280]) {
    for (const theme of ["light", "dark"]) {
      const ctx = await browser.newContext({ viewport: { width, height: width === 390 ? 844 : 900 }, colorScheme: theme, deviceScaleFactor: 1 });
      const page = await ctx.newPage();
      const errors = [];
      page.on("console", (m) => { if (m.type() === "error" || m.type() === "warning") errors.push(m.type() + ": " + m.text()); });
      page.on("pageerror", (e) => errors.push("pageerror: " + e.message));
      await page.goto(url);
      const tag = `${width}-${theme === "light" ? "claro" : "escuro"}`;
      // Rolagem horizontal: a página nunca pode ser mais larga que a janela.
      const over = await page.evaluate(() => {
        const w = document.documentElement.clientWidth, out = [];
        document.querySelectorAll("body *").forEach((el) => {
          const r = el.getBoundingClientRect();
          if (r.width && r.right > w + 1 && !el.closest(".table-scroll, .diagram-scroll, pre, .nav")) out.push(el.tagName + "." + el.className + " " + Math.round(r.right));
        });
        return { scroll: document.documentElement.scrollWidth, w, out: out.slice(0, 8) };
      });
      if (over.scroll > over.w) fail(`${tag}: rolagem horizontal (${over.scroll} > ${over.w}): ${over.out.join(", ")}`);
      await page.screenshot({ path: `${shots}/${tag}-inicio.png` });
      // Busca.
      if (width === 390) { await page.click(".menu-btn"); }
      await page.fill("#busca", "fix_trigger");
      const hits = await page.$$eval("#resultados li a", (l) => l.length);
      if (hits < 3) fail(`${tag}: busca por fix_trigger trouxe ${hits}`);
      if (width === 390) { await page.screenshot({ path: `${shots}/${tag}-busca.png` }); await page.fill("#busca", ""); await page.click(".menu-btn"); }
      else await page.fill("#busca", "");
      // Simulador.
      await page.goto(url + "#simulador-de-roteamento");
      await page.selectOption("[data-sim-select]", "docker-longo");
      const sim = await page.textContent("[data-sim-out]");
      if (!/plataforma/.test(sim) || !/override/.test(sim)) fail(`${tag}: simulador sem plataforma/override: ${sim.slice(0, 200)}`);
      await page.selectOption("[data-sim-select]", "cruza");
      const cruza = await page.textContent("[data-sim-out]");
      if (!/identidade/.test(cruza) || !/Também afeta/.test(cruza)) fail(`${tag}: simulador sem o secundário`);
      await page.locator("[data-sim]").scrollIntoViewIfNeeded();
      await page.evaluate(() => document.querySelector("#simulador-de-roteamento").scrollIntoView());
      await page.screenshot({ path: `${shots}/${tag}-simulador.png` });
      // Gerador: time novo válido, depois inválido.
      await page.evaluate(() => document.querySelector("#gerador-de-config").scrollIntoView());
      await page.click("#t-gen-time");
      const ok1 = await page.textContent("#gen-time [data-gen-status]");
      if (!/Válido/.test(ok1)) fail(`${tag}: gerador do time não validou: ${ok1}`);
      const yaml = await page.textContent("#gen-time [data-gen-yaml]");
      if (!/modules:/.test(yaml) || !/linear:/.test(yaml)) fail(`${tag}: yaml do time sem modules/linear`);
      await page.fill("#time-assignee", "x").catch(() => {});
      await page.fill("#time-nome", "Financeiro Novo");
      await page.fill("#time-modulos", "acme/plataforma");
      const bad = await page.textContent("#gen-time [data-gen-status]");
      if (!/problema/.test(bad)) fail(`${tag}: gerador não mostrou o erro: ${bad}`);
      await page.evaluate(() => document.querySelector("#gen-time").scrollIntoView());
      await page.screenshot({ path: `${shots}/${tag}-gerador.png` });
      await page.fill("#time-nome", "financeiro");
      await page.fill("#time-modulos", "acme/plataforma | modules/faturamento/** | Financeiro · Faturamento");
      await page.click("#t-gen-pequena");
      const ok2 = await page.textContent("#gen-pequena [data-gen-status]");
      if (!/Válido/.test(ok2)) fail(`${tag}: gerador da empresa pequena não validou: ${ok2}`);
      // Erros comuns: filtro.
      await page.goto(url + "#erros-comuns");
      await page.fill("#erros-comuns ~ * [data-filter-input], .sec#erros-comuns [data-filter-input]", "policy.auto_approve");
      const count = await page.textContent(".sec#erros-comuns [data-filter-count]");
      if (!/^[1-3] de /.test(count)) fail(`${tag}: filtro de erros: ${count}`);
      await page.screenshot({ path: `${shots}/${tag}-erros.png` });
      // Diagrama dos modos.
      await page.goto(url + "#diagramas");
      await page.screenshot({ path: `${shots}/${tag}-modos.png` });
      // Copiar.
      const btns = await page.$$eval(".copy", (b) => b.length);
      if (btns < 20) fail(`${tag}: só ${btns} botões de copiar`);
      if (errors.length) fail(`${tag}: console: ${errors.join(" | ")}`);
      console.log(`${tag}: ok (largura ${over.scroll}/${over.w}, ${hits} resultados, ${btns} botões de copiar, erros: ${count})`);
      await ctx.close();
    }
  }
  await browser.close();
  process.exit(failures ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(2); });
