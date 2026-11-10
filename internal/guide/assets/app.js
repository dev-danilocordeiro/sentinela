(function () {
  "use strict";
  var data = {};
  try { data = JSON.parse(document.getElementById("guia-dados").textContent); } catch (e) { data = {}; }
  var $ = function (sel, el) { return (el || document).querySelector(sel); };
  var $$ = function (sel, el) { return Array.prototype.slice.call((el || document).querySelectorAll(sel)); };
  function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch (e) { return null; } return null; }
  function escHTML(s) { return String(s).replace(/[&<>"']/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]; }); }

  // Tema: segue o sistema; o botão fixa claro ou escuro e lembra neste navegador.
  var saved = store("guia-tema");
  if (saved === "light" || saved === "dark") document.documentElement.setAttribute("data-theme", saved);
  $(".theme-btn").addEventListener("click", function () {
    var cur = document.documentElement.getAttribute("data-theme");
    if (!cur) cur = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
    var next = cur === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    store("guia-tema", next);
  });

  // Navegação no celular.
  var nav = $("#nav"), menu = $(".menu-btn");
  menu.addEventListener("click", function () {
    var open = nav.classList.toggle("open");
    menu.setAttribute("aria-expanded", open ? "true" : "false");
  });
  nav.addEventListener("click", function (e) {
    if (e.target.closest("a")) { nav.classList.remove("open"); menu.setAttribute("aria-expanded", "false"); }
  });

  // Copiar: em todo bloco de código e yaml.
  function copyText(text, btn) {
    function done() { btn.textContent = "copiado"; btn.classList.add("done"); setTimeout(function () { btn.textContent = "copiar"; btn.classList.remove("done"); }, 1500); }
    function fallback() {
      var ta = document.createElement("textarea");
      ta.value = text; ta.setAttribute("readonly", ""); ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.select();
      try { document.execCommand("copy"); } catch (e) { /* sem cópia */ }
      document.body.removeChild(ta); done();
    }
    if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(done, fallback); else fallback();
  }
  function addCopy(box) {
    if (box.querySelector(":scope > .copy")) return;
    var btn = document.createElement("button");
    btn.type = "button"; btn.className = "copy"; btn.textContent = "copiar";
    btn.addEventListener("click", function () {
      var raw = box.querySelector("pre.raw code") || box.querySelector("pre code");
      var text = raw ? raw.textContent : "";
      // Num bloco de terminal, copia só os comandos (linhas com $), sem a saída.
      if (/^\$ /.test(text)) text = text.split("\n").filter(function (l) { return l.indexOf("$ ") === 0; }).map(function (l) { return l.slice(2); }).join("\n");
      copyText(text, btn);
    });
    box.appendChild(btn);
  }
  $$(".content pre").forEach(function (pre) {
    if (pre.classList.contains("raw")) return;
    var box = pre.closest(".code, .annotated, .err-card");
    if (!box) {
      box = document.createElement("div"); box.className = "code";
      pre.parentNode.insertBefore(box, pre); box.appendChild(pre);
      // Uma barra de título, para o botão de copiar não cobrir o código.
      var lang = ((pre.querySelector("code") || {}).className || "").replace("language-", "");
      var bar = document.createElement("div"); bar.className = "code-title";
      bar.textContent = { bash: "terminal", sh: "terminal", console: "terminal", "": "texto" }[lang] || lang;
      box.insertBefore(bar, pre);
    }
    addCopy(box);
  });
  $$(".annotated").forEach(addCopy);

  // Abas.
  $$("[data-tabs]").forEach(function (tabs) {
    var buttons = $$(":scope > .tab-list > button", tabs);
    buttons.forEach(function (btn) {
      btn.addEventListener("click", function () {
        buttons.forEach(function (b) {
          var on = b === btn;
          b.setAttribute("aria-selected", on ? "true" : "false");
          document.getElementById(b.getAttribute("aria-controls")).hidden = !on;
        });
      });
    });
  });

  // Busca: um índice por título (h2 a h4) com o texto até o próximo título.
  var index = [];
  $$(".content h2[id], .content h3[id], .content h4[id]").forEach(function (h) {
    var text = "", el = h.nextElementSibling;
    while (el && !/^H[2-4]$/.test(el.tagName)) { text += " " + el.textContent; el = el.nextElementSibling; }
    var sec = h.closest(".sec"), top = sec ? sec.querySelector("h2") : null;
    index.push({ id: h.id, title: h.textContent, where: top && top !== h ? top.textContent : "", text: text.replace(/\s+/g, " ") });
  });
  function norm(s) { return s.toLowerCase().normalize("NFD").replace(/[̀-ͯ]/g, ""); }
  index.forEach(function (it) { it.n = norm(it.title + " " + it.text); it.nt = norm(it.title); });
  var busca = $("#busca"), results = $("#resultados"), toc = $(".toc");
  busca.addEventListener("input", function () {
    var q = norm(busca.value.trim());
    if (q.length < 2) { results.hidden = true; toc.hidden = false; return; }
    var words = q.split(/\s+/);
    var hits = index.filter(function (it) { return words.every(function (w) { return it.n.indexOf(w) >= 0; }); });
    hits.sort(function (a, b) { return (b.nt.indexOf(words[0]) >= 0) - (a.nt.indexOf(words[0]) >= 0); });
    results.innerHTML = hits.slice(0, 30).map(function (it) {
      var i = it.n.indexOf(words[0]) - it.title.length - 1, snip = "";
      if (i >= 0) { snip = it.text.slice(Math.max(0, i - 40), i + 80); }
      return '<li><a href="#' + it.id + '">' + escHTML(it.title) + "<small>" + escHTML(it.where) + (snip ? " · …" + escHTML(snip) + "…" : "") + "</small></a></li>";
    }).join("") || "<li><small>Nada encontrado.</small></li>";
    results.hidden = false; toc.hidden = true;
  });

  // Seção atual destacada na navegação.
  var links = {};
  $$(".toc a").forEach(function (a) { links[a.getAttribute("href").slice(1)] = a; });
  if ("IntersectionObserver" in window) {
    var obs = new IntersectionObserver(function (entries) {
      entries.forEach(function (en) {
        if (en.isIntersecting && links[en.target.id]) {
          $$(".toc a.active").forEach(function (a) { a.classList.remove("active"); });
          links[en.target.id].classList.add("active");
        }
      });
    }, { rootMargin: "-60px 0px -70% 0px" });
    $$(".content h2[id], .content h3[id]").forEach(function (h) { obs.observe(h); });
  }

  // Filtros (erros, referência, comandos).
  $$("[data-filter]").forEach(function (box) {
    var input = $("[data-filter-input]", box), count = $("[data-filter-count]", box), items = $$("[data-filter-item]", box);
    var texts = items.map(function (it) { return norm(it.textContent); });
    function apply() {
      var q = norm(input.value.trim()), words = q ? q.split(/\s+/) : [], n = 0;
      items.forEach(function (it, i) {
        var show = words.every(function (w) { return texts[i].indexOf(w) >= 0; });
        it.hidden = !show; if (show) n++;
      });
      count.textContent = n + " de " + items.length;
    }
    input.addEventListener("input", apply); apply();
  });

  // Simulador de roteamento: só mostra as saídas reais do owners explain geradas no build.
  var sims = data.simulator || [];
  $$("[data-sim]").forEach(function (box) {
    var sel = $("[data-sim-select]", box), out = $("[data-sim-out]", box);
    function show() {
      var c = sims.filter(function (s) { return s.id === sel.value; })[0];
      if (!c) return;
      var html = '<div class="sim-card"><p class="why">' + escHTML(c.why) + '</p><dl class="sim-grid">';
      c.fields.forEach(function (f) {
        html += "<dt>" + escHTML(f.name) + "</dt><dd>" + (f.value ? "<code>" + escHTML(f.value) + "</code>" : "—") + (f.source ? "<small>de " + escHTML(f.source) + "</small>" : "") + "</dd>";
      });
      html += "</dl>";
      if (c.secondary && c.secondary.length) {
        html += '<div class="sim-sec"><strong>Times secundários (Também afeta)</strong><ul>' + c.secondary.map(function (s) { return "<li>" + escHTML(s) + "</li>"; }).join("") + "</ul></div>";
      }
      html += '<details class="raw-out" open><summary>Saída real do comando</summary><div class="code"><pre><code>' + escHTML("$ " + c.command + "\n" + c.output) + "</code></pre></div></details></div>";
      out.innerHTML = html;
      $$(".code", out).forEach(addCopy);
    }
    sel.addEventListener("change", show); show();
  });

  // Validador de JSON Schema (o subconjunto que o schema do Sentinela usa).
  function typeOf(v) { if (v === null) return "null"; if (Array.isArray(v)) return "array"; if (typeof v === "number") return Number.isInteger(v) ? "integer" : "number"; return typeof v; }
  function isType(t, v) { var k = typeOf(v); return t === k || (t === "number" && k === "integer"); }
  function validate(schema, v, path, errs) {
    var where = path || "(raiz)";
    if (schema.oneOf) {
      var ok = schema.oneOf.filter(function (s) { var e = []; validate(s, v, path, e); return e.length === 0; }).length;
      if (ok !== 1) errs.push(where + ": valor fora das opções aceitas");
      return;
    }
    if (schema.type) {
      var types = [].concat(schema.type);
      if (!types.some(function (t) { return isType(t, v); })) { errs.push(where + ": é " + typeOf(v) + ", deveria ser " + types.join(" ou ")); return; }
    }
    if (schema.enum && schema.enum.indexOf(v) < 0) errs.push(where + ": " + JSON.stringify(v) + " não é um de " + schema.enum.join(", "));
    if (schema.pattern && typeof v === "string" && !new RegExp(schema.pattern, "u").test(v)) errs.push(where + ": " + JSON.stringify(v) + " não tem o formato esperado");
    if (typeOf(v) === "object") {
      var props = schema.properties || {};
      (schema.required || []).forEach(function (k) { if (!(k in v)) errs.push(where + ": falta a chave " + k); });
      Object.keys(v).forEach(function (k) {
        var p = path ? path + "." + k : k;
        if (props[k]) validate(props[k], v[k], p, errs);
        else if (schema.additionalProperties === false) errs.push(p + ": chave que não existe no schema");
        else if (schema.additionalProperties && typeof schema.additionalProperties === "object") validate(schema.additionalProperties, v[k], p, errs);
      });
    }
    if (Array.isArray(v) && schema.items) v.forEach(function (x, i) { validate(schema.items, x, path + "[" + i + "]", errs); });
  }

  // YAML a partir do objeto (blocos; objetos pequenos de escalares em uma linha, como nos exemplos).
  var plainRe = /^[A-Za-z_][A-Za-z0-9_.\/-]*$/, reserved = /^(true|false|yes|no|on|off|null|y|n)$/i;
  function scalar(v) {
    if (typeof v === "string") return plainRe.test(v) && !reserved.test(v) ? v : JSON.stringify(v);
    return String(v);
  }
  function isScalar(v) { return v === null || typeof v !== "object"; }
  function flow(v) {
    if (Array.isArray(v)) return "[" + v.map(scalar).join(", ") + "]";
    return "{ " + Object.keys(v).map(function (k) { return k + ": " + scalar(v[k]); }).join(", ") + " }";
  }
  function canFlow(v) {
    var vals = Array.isArray(v) ? v : Object.keys(v).map(function (k) { return v[k]; });
    return vals.length > 0 && vals.every(isScalar) && flow(v).length <= 100;
  }
  function yaml(v, ind) {
    ind = ind || "";
    var out = [];
    if (Array.isArray(v)) {
      v.forEach(function (x) {
        if (isScalar(x)) out.push(ind + "- " + scalar(x));
        else if (canFlow(x)) out.push(ind + "- " + flow(x));
        else { var sub = yaml(x, ind + "  "); out.push(ind + "- " + sub[0].trim()); out = out.concat(sub.slice(1)); }
      });
      return out;
    }
    Object.keys(v).forEach(function (k) {
      var x = v[k];
      if (isScalar(x)) out.push(ind + k + ": " + scalar(x));
      else if (canFlow(x)) out.push(ind + k + ": " + flow(x));
      else { out.push(ind + k + ":"); out = out.concat(yaml(x, ind + "  ")); }
    });
    return out;
  }
  function lines(s) { return s.split("\n").map(function (l) { return l.trim(); }).filter(Boolean); }
  function clone(o) { return JSON.parse(JSON.stringify(o)); }

  var builders = {
    // Empresa pequena: parte do exemplo real (examples/empresa-pequena) e troca o que o formulário diz.
    pequena: function (f) {
      var c = clone((data.bases || {}).pequena || {});
      var team = f.time || "engenharia";
      c.adapters = f.modo === "completo" ? { tracker: "linear_api", notifier: "slack_api", git_host: "github_apps" }
        : f.modo === "mcp" ? { tracker: "linear_mcp", notifier: "none", git_host: "gh_cli" }
        : { tracker: "local", notifier: "none", git_host: "gh_cli" };
      c.defaults = c.defaults || {};
      c.defaults.linear = { team: f.linear };
      if (f.projeto) c.defaults.linear.project = f.projeto;
      c.defaults.linear.labels = ["sentinela"]; c.defaults.linear.triage_state = "Triage";
      if (f.modo === "local") { delete c.defaults.notify; } else if (f.canal) { c.defaults.notify = { slack: f.canal }; } else { delete c.defaults.notify; }
      c.labels = c.labels || {}; c.labels.trigger = f.label_gatilho;
      if (f.gatilho === "lista") c.fix_trigger = { allowed: lines(f.lista) };
      else c.fix_trigger = { allowed: f.gatilho };
      if (f.modo !== "completo") delete c.github_apps;
      c.llm = { provider: f.provider };
      c.budget = c.budget || {}; c.budget.per_run_usd = Number(f.per_run); c.budget.per_pr_usd = Number(f.per_pr);
      var t = { github: lines(f.github.replace(/,/g, "\n")), linear: { team: f.linear } };
      if (f.grupo && f.modo !== "local") t.notify = { mention: f.grupo };
      c.teams = {}; c.teams[team] = t;
      c.publish = { enabled: f.modo !== "local", max_issues_per_team_per_run: 3 };
      c.repos = lines(f.repos).map(function (r) { return { name: r, team: team }; });
      return { file: "sentinela.yaml", obj: c };
    },
    time: function (f) {
      var t = {};
      if (f.github) t.github = lines(f.github.replace(/,/g, "\n"));
      if (f.assignee !== "none") t.assignee = f.assignee;
      if (f.lead) t.lead = f.lead;
      t.linear = { team: f.linear };
      if (f.projeto) t.linear.project = f.projeto;
      if (f.seguranca) t.linear.category_projects = { security: f.seguranca };
      if (f.canal || f.grupo) { t.notify = {}; if (f.canal) t.notify.slack = f.canal; if (f.grupo) t.notify.mention = f.grupo; }
      var lb = {};
      if (f.label_gatilho) lb.trigger = f.label_gatilho;
      if (f.label_done) lb.done = f.label_done;
      if (f.label_failed) lb.failed = f.label_failed;
      if (Object.keys(lb).length) t.labels = lb;
      var mods = lines(f.modulos).map(function (l) {
        var p = l.split("|").map(function (x) { return x.trim(); });
        var m = { repo: p[0], path: p[1] || "" };
        if (p[2]) m.linear_project = p[2];
        if (p[3] && /^(override|true|sim)$/i.test(p[3])) m.override = true;
        return m;
      });
      if (mods.length) t.modules = mods;
      var repos = lines(f.repos).map(function (r) { return { name: r }; });
      if (repos.length) t.repos = repos;
      return { file: "config.d/teams/" + (f.nome || "time") + ".yaml", obj: t };
    }
  };

  $$("form[data-gen]").forEach(function (form) {
    var kind = form.getAttribute("data-gen"), schema = (data.schemas || {})[form.getAttribute("data-schema")];
    var status = $("[data-gen-status]", form), code = $("[data-gen-yaml]", form), file = $("[data-gen-file]", form);
    function run() {
      var f = {};
      $$("input, select, textarea", form).forEach(function (el) { f[el.name] = el.value.trim(); });
      var res = builders[kind](f), errs = [];
      if (kind === "time" && !/^[a-z0-9][a-z0-9_-]*$/.test(f.nome)) errs.push("nome do time: use minúsculas, números, - ou _ (é o nome do arquivo)");
      if (kind === "time") (res.obj.modules || []).forEach(function (m, i) { if (!m.path) errs.push("modules[" + i + "]: falta o caminho (use repo | caminho | projeto)"); });
      if (schema) validate(schema, res.obj, "", errs);
      var head = kind === "time"
        ? "# " + res.file + ": gerado no guia do Sentinela. Confira com config validate --offline antes do PR.\n"
        : "# sentinela.yaml: gerado no guia do Sentinela a partir de examples/empresa-pequena. Confira com config validate.\n";
      code.textContent = head + yaml(res.obj).join("\n") + "\n";
      file.textContent = res.file;
      if (errs.length) {
        status.className = "gen-status bad";
        status.innerHTML = "<strong>" + errs.length + " problema(s) contra o JSON Schema:</strong><ul>" + errs.map(function (e) { return "<li>" + escHTML(e) + "</li>"; }).join("") + "</ul>";
      } else {
        status.className = "gen-status ok";
        status.innerHTML = "<strong>Válido contra o JSON Schema embutido</strong> (" + (kind === "time" ? "schema/team.schema.json" : "schema/sentinela.schema.json") + ").";
      }
    }
    form.addEventListener("input", run);
    form.addEventListener("submit", function (e) { e.preventDefault(); });
    run();
  });

  // Abrir direto numa âncora dentro de uma aba fechada.
  function reveal() {
    var id = decodeURIComponent(location.hash.slice(1)); if (!id) return;
    var el = document.getElementById(id); if (!el) return;
    var panel = el.closest(".tab-panel");
    if (panel && panel.hidden) { var btn = document.getElementById("t-" + panel.id); if (btn) btn.click(); el.scrollIntoView(); }
  }
  window.addEventListener("hashchange", reveal); reveal();
})();
