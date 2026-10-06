// ship UI: small, dependency-free glue around htmx, its SSE extension and elkjs.
(function () {
  "use strict";
  var page = document.body.dataset.page;
  var runID = document.body.dataset.run;

  // ---- theme ---------------------------------------------------------------
  document.addEventListener("click", function (e) {
    if (!e.target.closest(".theme-toggle")) return;
    var root = document.documentElement;
    var dark = root.dataset.theme
      ? root.dataset.theme === "dark"
      : window.matchMedia("(prefers-color-scheme: dark)").matches;
    root.dataset.theme = dark ? "light" : "dark";
    try { localStorage.setItem("ship-theme", root.dataset.theme); } catch (err) {}
  });

  // Version pickers on the pipeline page compare as soon as you pick.
  document.addEventListener("change", function (e) {
    if (e.target.matches && e.target.matches("[data-autosubmit]") && e.target.form) e.target.form.submit();
  });

  // ---- toasts --------------------------------------------------------------
  function toast(msg, ok) {
    var box = document.getElementById("toasts");
    var el = document.createElement("div");
    el.className = "toast" + (ok ? " ok" : "");
    el.textContent = msg;
    box.appendChild(el);
    setTimeout(function () { el.remove(); }, ok ? 2500 : 9000);
  }
  document.addEventListener("htmx:responseError", function (e) {
    var xhr = e.detail.xhr, msg = "Request failed (" + xhr.status + ")";
    try {
      var body = JSON.parse(xhr.responseText);
      if (body.error && body.error.message) msg = body.error.message;
      if (body.errors && body.errors.length) {
        msg += "\n" + body.errors.slice(0, 6).map(function (f) {
          return f.code + " line " + f.line + ": " + f.message;
        }).join("\n");
      }
    } catch (err) {}
    toast(msg);
  });
  document.addEventListener("htmx:sendError", function () { toast("Can't reach the ship daemon. Is it still running?"); });
  document.addEventListener("htmx:afterRequest", function (e) {
    var form = e.detail.elt.closest && e.detail.elt.closest("form.var-form");
    if (form && e.detail.successful) toast("Variable set", true);
    var ask = e.detail.elt.closest && e.detail.elt.closest("[data-ask-form]");
    if (ask && e.detail.successful) {
      ask.reset();
      (ask.closest(".action") || document).querySelectorAll("[data-finding-note]").forEach(function (t) { t.value = ""; });
    }
    if (e.detail.elt.hasAttribute && e.detail.elt.hasAttribute("data-backfill") && e.detail.successful) {
      try {
        var rec = JSON.parse(e.detail.xhr.responseText).recorded || 0;
        toast("Recorded " + rec + " past run" + (rec === 1 ? "" : "s"), true);
      } catch (err) {}
      setTimeout(function () { location.reload(); }, 600);
    }
    if (e.detail.elt.hasAttribute && e.detail.elt.hasAttribute("data-prune") && e.detail.successful) {
      try {
        var res = JSON.parse(e.detail.xhr.responseText), n = (res.pruned || []).length, kept = (res.skipped || []).length;
        var msg = n ? "Deleted " + n + " run" + (n === 1 ? "" : "s") + ", freeing " + bytes(res.bytes) : "Nothing to prune";
        if (kept) msg += ". Kept " + kept + " that still " + (kept === 1 ? "has a worktree" : "have worktrees") + " (ship prune --force)";
        toast(msg, true);
      } catch (err) {}
    }
  });

  function bytes(n) {
    var units = ["B", "KB", "MB", "GB", "TB"], i = 0;
    while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(1) : n) + " " + units[i];
  }

  // ---- check-ins -------------------------------------------------------------
  // Calls on individual findings are added to the note, one line each.
  document.addEventListener("htmx:configRequest", function (e) {
    var form = e.detail.elt.closest && e.detail.elt.closest("[data-ask-form]");
    if (!form) return;
    var card = form.closest(".action") || document;
    var lines = [];
    card.querySelectorAll("[data-finding-note]").forEach(function (t) {
      var v = t.value.trim();
      if (v) lines.push(t.dataset.findingNote + ": " + v);
    });
    if (!lines.length) return;
    var note = (e.detail.parameters.note || "").trim();
    e.detail.parameters.note = (note ? note + "\n\n" : "") + lines.join("\n");
  });
  // Refreshes of the action card mustn't lose what you've typed or folded.
  var actionState = null;
  document.addEventListener("htmx:beforeSwap", function (e) {
    if (e.detail.target.id !== "run-action") return;
    actionState = { vals: {}, closed: {} };
    e.detail.target.querySelectorAll("textarea, input:not([type=hidden])").forEach(function (el) {
      if (el.type === "radio") {
        if (el.checked) actionState.vals["radio:" + el.name] = el.value;
        return;
      }
      var k = el.id || el.name;
      if (k && el.value) actionState.vals[k] = el.value;
    });
    e.detail.target.querySelectorAll(".finding[id] > details:not([open])").forEach(function (d) {
      actionState.closed[d.parentNode.id] = true;
    });
  });
  document.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target.id !== "run-action" || !actionState) return;
    var st = actionState;
    actionState = null;
    e.detail.target.querySelectorAll("textarea, input:not([type=hidden])").forEach(function (el) {
      if (el.type === "radio") {
        var picked = st.vals["radio:" + el.name];
        if (picked) el.checked = el.value === picked;
        return;
      }
      var k = el.id || el.name;
      if (k && st.vals[k] && !el.value) el.value = st.vals[k];
    });
    Object.keys(st.closed).forEach(function (id) {
      var d = document.getElementById(id);
      if (d && d.firstElementChild) d.firstElementChild.open = false;
    });
  });
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-fill]");
    if (!b) return;
    var t = b.closest(".finding-note").querySelector("textarea");
    t.value = b.dataset.fill;
    t.focus();
  });

  // ---- since you last looked ---------------------------------------------------
  // Each browser remembers what a run looked like when its page was last
  // open (visits, commits, visits per step); check-in cards say what changed
  // since. The run page compares with what was remembered when it loaded.
  var seenBase = {};
  function seenKey(id) { return "ship-seen:" + id; }
  function loadSeen(id) {
    try { return JSON.parse(localStorage.getItem(seenKey(id)) || "null"); } catch (err) { return null; }
  }
  function plural(n, s) { return n + " " + s + (n === 1 ? "" : "s"); }
  function showChanged(root) {
    var page = document.body.dataset.page;
    (root || document).querySelectorAll("[data-seen-run]").forEach(function (p) {
      var id = p.dataset.seenRun, now;
      try { now = JSON.parse(p.dataset.seen); } catch (err) { return; }
      var then = page === "run" && id === document.body.dataset.run ? seenBase[id] : loadSeen(id);
      if (!then) { p.hidden = true; return; }
      var parts = [];
      var c = now.commits - then.commits;
      if (now.commits >= 0 && then.commits >= 0 && c > 0) parts.push(plural(c, "new commit"));
      var from = p.dataset.seenFrom, r = 0;
      if (from) {
        r = ((now.steps || {})[from] || 0) - ((then.steps || {})[from] || 0);
        if (r > 0) parts.push(plural(r, "more round") + " of " + from);
      }
      var v = now.visits - then.visits;
      if (v > r) parts.push(plural(v - r, r ? "other visit" : "new visit"));
      p.textContent = parts.length ? "Since you last looked: " + parts.join(" · ") : "Nothing new since you last looked.";
      p.hidden = false;
    });
  }
  function rememberRun() {
    var head = document.querySelector("[data-run-seen]");
    if (!head) return;
    var id = head.dataset.runSeen;
    if (!(id in seenBase)) seenBase[id] = loadSeen(id);
    try { localStorage.setItem(seenKey(id), head.dataset.seen); } catch (err) {}
  }
  document.addEventListener("DOMContentLoaded", function () { rememberRun(); showChanged(); });
  document.addEventListener("htmx:afterSwap", function (e) { rememberRun(); showChanged(e.detail.target); });

  // ---- copy ----------------------------------------------------------------
  document.addEventListener("click", function (e) {
    var b = e.target.closest("[data-copy]");
    if (!b) return;
    navigator.clipboard.writeText(b.dataset.copy).then(function () { toast("Copied", true); });
  });

  // ---- graphs ----------------------------------------------------------------
  var SVGNS = "http://www.w3.org/2000/svg";
  var GLYPH = { agent: "◆", run: "▢", ask: "●", wait: "⏳", split: "⑂", fanout: "☰", done: "✓", stop: "■" };
  function svg(tag, attrs, parent) {
    var el = document.createElementNS(SVGNS, tag);
    for (var k in attrs) el.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(el);
    return el;
  }
  function textWidth(s, px) { return Math.ceil(s.length * px * 0.62); }

  function renderGraph(box) {
    if (typeof ELK === "undefined") { setTimeout(function () { renderGraph(box); }, 100); return; }
    fetch(box.dataset.src, { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (g) {
      if (!g.nodes) throw new Error((g.error && g.error.message) || "no graph in the response");
      var elk = new ELK();
      // Catch-all check-ins (where errors and give-ups go) would connect to
      // every step; show them only when a run actually went there.
      var shown = {};
      var nodes = g.nodes.filter(function (n) {
        var keep = !n.fallback || n.visits > 0 || n.current;
        if (keep) shown[n.id] = true;
        return keep;
      });
      var edges = g.edges.filter(function (e) {
        return shown[e.from] && shown[e.to] && (e.taken || !e.fallback);
      });
      var graph = {
        id: "root",
        layoutOptions: {
          // Left to right, like a CI workflow; loops route back around.
          "elk.algorithm": "layered", "elk.direction": "RIGHT",
          "elk.layered.spacing.nodeNodeBetweenLayers": "48", "elk.spacing.nodeNode": "22",
          "elk.spacing.edgeLabel": "3", "elk.edgeRouting": "ORTHOGONAL",
          "elk.layered.cycleBreaking.strategy": "MODEL_ORDER",
          "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
          "elk.layered.nodePlacement.strategy": "BRANDES_KOEPF"
        },
        children: nodes.map(function (n) {
          var label = n.label + (n.visits ? "  ×" + n.visits : "");
          return { id: n.id, width: Math.max(64, textWidth(label, 12) + 38), height: 32, data: n };
        }),
        edges: edges.map(function (e, i) {
          return { id: "e" + i, sources: [e.from], targets: [e.to], data: e,
            labels: e.label && e.label !== "done" ? [{ text: e.label, width: textWidth(e.label, 10.5) + 4, height: 13 }] : [] };
        })
      };
      var hidden = g.nodes.length - nodes.length;
      return elk.layout(graph).then(function (out) { draw(box, out, hidden); });
    }).catch(function (err) { box.innerHTML = '<p class="muted">Graph unavailable: ' + String(err).replace(/</g, "&lt;") + "</p>"; });
  }

  function draw(box, out, hidden) {
    var pad = 8, w = out.width + pad * 2, h = out.height + pad * 2;
    var root = svg("svg", { viewBox: "0 0 " + w + " " + h, width: w, height: h, role: "group", "aria-label": "Pipeline graph: select a step for details" });
    var defs = svg("defs", {}, root);
    var m = svg("marker", { id: "arrow-" + Math.random().toString(36).slice(2), viewBox: "0 0 10 10", refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, orient: "auto-start-reverse" }, defs);
    svg("path", { d: "M0,0 L10,5 L0,10 z", class: "arrow" }, m);
    var g = svg("g", { transform: "translate(" + pad + "," + pad + ")" }, root);
    (out.edges || []).forEach(function (e) {
      var d = e.data, cls = "edge k-" + d.kind + (d.taken ? " taken" : "");
      var eg = svg("g", { class: cls }, g);
      (e.sections || []).forEach(function (s) {
        var pts = [s.startPoint].concat(s.bendPoints || [], [s.endPoint]);
        svg("path", { d: "M" + pts.map(function (p) { return p.x + "," + p.y; }).join(" L"), "marker-end": "url(#" + m.id + ")" }, eg);
      });
      (e.labels || []).forEach(function (l) {
        var t = svg("text", { x: l.x + 2, y: l.y + 10 }, eg);
        t.textContent = l.text;
      });
    });
    (out.children || []).forEach(function (n) {
      var d = n.data;
      var cls = "node t-" + d.type + (d.current ? " current" : "") + (d.visits ? " visited" : "");
      var ng = svg("g", { class: cls, transform: "translate(" + n.x + "," + n.y + ")", tabindex: 0, "data-step": d.id, role: "button", "aria-label": d.id + " (" + d.type + ")" }, g);
      var title = svg("title", {}, ng);
      title.textContent = d.id + " (" + d.type + ")" + (d.description ? ": " + d.description : "") + (d.visits ? " · " + d.visits + " visits" : "");
      svg("rect", { width: n.width, height: n.height, rx: d.type === "ask" ? 16 : 7 }, ng);
      var gl = svg("text", { x: 10, y: 20, class: "glyph" }, ng);
      gl.textContent = GLYPH[d.type] || "";
      var tx = svg("text", { x: 26, y: 20 }, ng);
      tx.textContent = d.label;
      if (d.visits) {
        var c = svg("text", { x: n.width - 8, y: 20, "text-anchor": "end", class: "count" }, ng);
        c.textContent = "×" + d.visits;
      }
    });
    box.innerHTML = "";
    box.appendChild(root);
    box._nodes = {};
    (out.children || []).forEach(function (n) { box._nodes[n.data.id] = n.data; });
    if (hidden) {
      var note = document.createElement("p");
      note.className = "graph-note muted";
      note.textContent = "Catch-all check-ins (reached on errors or when a step gives up) are hidden until a run goes there · dashed lines go back";
      box.appendChild(note);
    }
    // A refresh (live runs) keeps the open step panel, with fresh data.
    if (box._step) openStep(box, box._step);
    applyFilter();
  }

  function renderGraphs() {
    document.querySelectorAll(".graph[data-src]").forEach(renderGraph);
  }

  // ---- step panel ------------------------------------------------------------
  // Click a node to see what the step does; on a run page it also filters
  // the timeline to that step.
  var TYPE_NAME = { agent: "agent step", run: "run step", ask: "check-in", wait: "wait step", split: "split step",
    fanout: "fanout step", pr: "pull request step", done: "end of the run", stop: "the run stops" };

  function el(tag, cls, text, parent) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    if (parent) parent.appendChild(e);
    return e;
  }
  function ms(n) {
    if (!n) return "";
    if (n < 1000) return n + "ms";
    var s = Math.round(n / 1000);
    if (s < 60) return s + "s";
    var m = Math.floor(s / 60);
    if (m < 60) return m + "m " + (s % 60) + "s";
    return Math.floor(m / 60) + "h " + (m % 60) + "m";
  }
  function usd(n) { return n ? "$" + (n < 0.01 ? n.toFixed(4) : n.toFixed(2)) : ""; }

  function stepPanel(box, n) {
    var d = n.detail || {};
    var panel = el("section", "step-panel");
    panel.setAttribute("aria-label", "Step " + n.id);
    var head = el("header", "step-panel-head", null, panel);
    el("span", "glyph", GLYPH[n.type] || "", head).setAttribute("aria-hidden", "true");
    el("h3", null, n.id, head);
    el("span", "chip", TYPE_NAME[n.type] || n.type || "step", head);
    var close = el("button", "btn small step-close", "✕", head);
    close.type = "button";
    close.setAttribute("aria-label", "Close step details");
    close.addEventListener("click", function () { closeStep(box, true); });
    if (n.description) el("p", "step-desc", n.description, panel);

    var dl = null;
    function row(label, value) {
      if (value == null || value === "" || value === false || value === 0 || (value.length === 0)) return;
      if (!dl) dl = el("dl", "step-kv", null, panel);
      el("dt", null, label, dl);
      var dd = el("dd", null, null, dl);
      if (value === true) value = "yes";
      if (Array.isArray(value)) value = value.join(", ");
      el("code", null, String(value), dd);
    }
    function block(label, text) {
      if (!text) return;
      dl = null;
      el("h4", null, label, panel);
      el("pre", "step-pre", text, panel);
    }
    function pairs(list, sep) {
      return (list || []).map(function (p) { return p.value ? p.key + sep + p.value : p.key; });
    }

    switch (n.type) {
    case "agent":
    case "split":
      if (d.split) block("Split", d.split);
      row("Skill", d.agent);
      block("Prompt", d.prompt);
      block("Rules", d.rules);
      dl = null;
      row("Model", d.model); row("Effort", d.effort); row("CLI", d.cli);
      row("Permission mode", d.permission_mode); row("Allowed tools", d.allowed_tools);
      row("Disallowed tools", d.disallowed_tools); row("Session", d.session); row("Context", d.context);
      row("Saves", pairs(d.save, " ← ")); row("Saves on", d.save_on);
      row("Budget per visit", usd(d.max_budget_usd)); row("Tokens per visit", d.max_tokens ? d.max_tokens.toLocaleString() : "");
      row("Max slices", d.max_slices); row("Review slices", d.review);
      break;
    case "run":
      block("Script", d.run);
      row("Shell", d.shell); row("Exit codes", pairs(d.exit_codes, " → ")); row("Saves", pairs(d.save, " ← "));
      break;
    case "ask":
      block("Question", d.ask);
      row("Shows", d.show); row("Input", d.input);
      break;
    case "wait":
      block("Command", d.wait);
      row("Every", d.every);
      break;
    case "pr":
      block("Pull request", d.pr);
      row("Settle", d.settle); row("Trigger", d.trigger);
      break;
    case "fanout":
      block("Fanout", d.fanout);
      row("Mode", d.mode); row("Stack", d.stack); row("Advance on", d.advance_on);
      row("Max parallel", d.max_parallel); row("On child stop", d.on_child_stop);
      break;
    }

    if (d.routes && d.routes.length) {
      el("h4", null, n.type === "ask" ? "Choices" : "Where it goes", panel);
      var ul = el("ul", "step-routes", null, panel);
      d.routes.forEach(function (r) {
        var li = el("li", r.kind !== "outcome" ? "muted" : null, null, ul);
        el("code", null, r.outcome, li);
        li.append(" → ");
        el("strong", null, r.to, li);
      });
    }
    dl = null;
    row("Max visits", d.max_visits); row("When exhausted", d.when_exhausted);
    row("On error", d.on_error); row("Timeout", d.timeout);

    if (box.id === "graph" && n.detail) {
      el("h4", null, "Visits in this run", panel);
      var h = n.history || [];
      if (!h.length) el("p", "muted", "Not visited yet.", panel);
      var ol = el("ol", "step-visits", null, panel);
      h.forEach(function (v) {
        var li = el("li", null, null, ol);
        var a = el("a", null, "#" + v.number, li);
        a.href = "#"; a.dataset.openSeq = v.seq;
        a.title = "Open visit " + v.seq + " in the timeline";
        el("span", v.running ? "chip running" : "chip oc-" + v.outcome, v.running ? "running" : (v.outcome || "–"), li);
        var meta = [ms(v.duration_ms), usd(v.cost_usd)].filter(Boolean).join(" · ");
        if (meta) el("span", "muted", meta, li);
        if (v.summary) el("span", "step-visit-sum", v.summary.length > 280 ? v.summary.slice(0, 280) + "…" : v.summary, li);
      });
    }
    return panel;
  }

  function openStep(box, id) {
    var n = box._nodes && box._nodes[id];
    if (!n) { closeStep(box); return; }
    var old = box._panel, keep = old && old.contains(document.activeElement);
    box._panel = stepPanel(box, n);
    box._step = id;
    if (old) old.replaceWith(box._panel);
    else box.parentNode.insertBefore(box._panel, box.nextSibling);
    if (keep) box._panel.querySelector(".step-close").focus();
    box.querySelectorAll(".node").forEach(function (g) { g.classList.toggle("selected", g.dataset.step === id); });
  }
  function closeStep(box, refocus) {
    var id = box._step;
    if (box._panel) box._panel.remove();
    box._panel = null; box._step = "";
    box.querySelectorAll(".node.selected").forEach(function (g) { g.classList.remove("selected"); });
    if (box.id === "graph" && filterStep) { filterStep = ""; applyFilter(); }
    if (refocus && id) {
      var g = box.querySelector('.node[data-step="' + CSS.escape(id) + '"]');
      if (g) g.focus();
    }
  }
  function pickNode(node) {
    var box = node.closest(".graph"), id = node.dataset.step;
    if (box._step === id) { closeStep(box); return; }
    if (box.id === "graph") { filterStep = id; applyFilter(); }
    openStep(box, id);
    if (box._panel) box._panel.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }
  document.addEventListener("click", function (e) {
    var n = e.target.closest && e.target.closest(".graph .node");
    if (n) pickNode(n);
    var a = e.target.closest && e.target.closest("[data-open-seq]");
    if (!a) return;
    e.preventDefault();
    var v = document.querySelector('#run-timeline .visit[data-seq="' + a.dataset.openSeq + '"]');
    if (v) { v.open = true; v.scrollIntoView({ behavior: "smooth", block: "start" }); }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") {
      var open = Array.prototype.filter.call(document.querySelectorAll(".graph"), function (b) { return b._panel; });
      if (!open.length) return;
      var mine = open.filter(function (b) { return b._panel.contains(e.target) || b.contains(e.target); });
      // Focus goes back to the step only when it was in the graph or panel.
      (mine.length ? mine : open).forEach(function (b) { closeStep(b, mine.length > 0); });
      return;
    }
    if ((e.key === "Enter" || e.key === " ") && e.target.closest && e.target.closest(".graph .node")) {
      e.preventDefault();
      pickNode(e.target.closest(".graph .node"));
    }
  });

  var filterStep = "";
  function applyFilter() {
    document.querySelectorAll(".visit").forEach(function (v) {
      v.parentElement.classList.toggle("hidden-by-filter", !!filterStep && v.dataset.step !== filterStep);
      v.parentElement.style.display = filterStep && v.dataset.step !== filterStep ? "none" : "";
    });
    document.querySelectorAll("#graph .node").forEach(function (n) {
      n.classList.toggle("dim", !!filterStep && n.dataset.step !== filterStep);
    });
    var label = document.getElementById("timeline-filter");
    if (label) {
      label.innerHTML = "";
      if (filterStep) {
        label.append("Only “" + filterStep + "” · ");
        var a = document.createElement("a");
        a.href = "#"; a.textContent = "show all";
        a.addEventListener("click", function (e) {
          e.preventDefault();
          filterStep = "";
          var box = document.getElementById("graph");
          if (box) closeStep(box);
          applyFilter();
        });
        label.append(a);
      }
    }
  }

  // ---- timeline tabs ---------------------------------------------------------
  var openState = {}; // seq → {tab, pane, live}

  function loadTab(details, tab) {
    var seq = details.dataset.seq;
    var pane = details.querySelector("[data-pane]");
    details.querySelectorAll(".tabs button").forEach(function (b) {
      b.setAttribute("aria-selected", b.dataset.tab === tab ? "true" : "false");
    });
    pane.dataset.tab = tab;
    fetch("/fragments/runs/" + encodeURIComponent(runID) + "/visits/" + seq + "/" + tab, { credentials: "same-origin" })
      .then(function (r) { return r.text(); })
      .then(function (html) {
        pane.innerHTML = html;
        stickToBottom(pane.querySelector(".log, .feed"), true);
      });
    openState[seq] = { tab: tab, pane: pane, live: details.classList.contains("live") };
  }

  document.addEventListener("toggle", function (e) {
    var d = e.target;
    if (!d.classList || !d.classList.contains("visit")) return;
    if (d.open) {
      if (!d.querySelector("[data-pane]").dataset.tab) loadTab(d, "live");
    } else {
      delete openState[d.dataset.seq];
    }
  }, true);

  document.addEventListener("click", function (e) {
    var b = e.target.closest(".visit .tabs button");
    if (!b) return;
    loadTab(b.closest(".visit"), b.dataset.tab);
  });

  // Keep open visits (and their scroll) across timeline refreshes.
  document.addEventListener("htmx:beforeSwap", function (e) {
    if (e.detail.target.id !== "run-timeline") return;
    document.querySelectorAll("#run-timeline .visit[open]").forEach(function (d) {
      var st = openState[d.dataset.seq];
      if (st) st.pane = d.querySelector("[data-pane]");
    });
  });
  document.addEventListener("htmx:afterSwap", function (e) {
    var id = e.detail.target.id;
    if (id === "run-timeline") {
      Object.keys(openState).forEach(function (seq) {
        var st = openState[seq];
        var d = document.querySelector('#run-timeline .visit[data-seq="' + seq + '"]');
        if (!d) { delete openState[seq]; return; }
        var fresh = d.querySelector("[data-pane]");
        if (st.pane && st.pane !== fresh) fresh.replaceWith(st.pane);
        d.querySelectorAll(".tabs button").forEach(function (b) {
          b.setAttribute("aria-selected", b.dataset.tab === st.tab ? "true" : "false");
        });
        d.open = true;
        // A visit that just finished: refresh its tab once to pick up the handover/result.
        if (st.live && !d.classList.contains("live")) loadTab(d, st.tab);
        st.live = d.classList.contains("live");
      });
      applyFilter();
    }
    if (id === "run-header" || id === "run-timeline") scheduleGraph();
  });

  // ---- live streams ----------------------------------------------------------
  function nearBottom(el) { return el.scrollHeight - el.scrollTop - el.clientHeight < 40; }
  function stickToBottom(el, force) {
    if (!el) return;
    if (force || el.dataset.stick !== "false") el.scrollTop = el.scrollHeight;
  }
  document.addEventListener("scroll", function (e) {
    var el = e.target;
    if (el.classList && (el.classList.contains("log") || el.classList.contains("feed"))) {
      el.dataset.stick = nearBottom(el) ? "true" : "false";
    }
  }, true);

  function onOutput(msg) {
    var pre = document.querySelector('.log[data-live="output"][data-seq="' + msg.seq + '"]');
    if (!pre || msg.run !== runID) return;
    var stick = nearBottom(pre);
    var empty = pre.parentElement.querySelector(".log-empty");
    if (empty) empty.remove();
    if (msg.stream === "stderr") {
      var span = document.createElement("span");
      span.className = "err-chunk";
      span.textContent = msg.chunk;
      pre.appendChild(span);
    } else {
      pre.appendChild(document.createTextNode(msg.chunk));
    }
    if (stick) pre.scrollTop = pre.scrollHeight;
  }

  function summarise(input) {
    if (!input) return "";
    var keys = ["command", "file_path", "path", "pattern", "url", "description", "prompt", "outcome"];
    for (var i = 0; i < keys.length; i++) {
      if (typeof input[keys[i]] === "string") return input[keys[i]].slice(0, 140);
    }
    return "";
  }

  function onAgent(msg) {
    var feed = document.querySelector('.feed[data-live="agent"][data-seq="' + msg.seq + '"]');
    if (!feed || msg.run !== runID) return;
    var stick = nearBottom(feed), el;
    var empty = feed.querySelector(".feed-empty");
    if (empty) empty.remove();
    var d = msg.data || {};
    if (msg.kind === "text") {
      el = document.createElement("div");
      el.className = "say";
      el.textContent = d.text || "";
    } else if (msg.kind === "tool_use") {
      el = document.createElement("details");
      el.className = "tool";
      var s = document.createElement("summary");
      var name = document.createElement("span");
      name.className = "tool-name"; name.textContent = d.name || "tool";
      var rest = document.createElement("span");
      rest.className = "muted"; rest.textContent = " " + summarise(d.input);
      s.append(name, rest);
      var pre = document.createElement("pre");
      pre.textContent = JSON.stringify(d.input, null, 2);
      el.append(s, pre);
    } else if (msg.kind === "tool_result") {
      el = document.createElement("details");
      el.className = "tool-result" + (d.is_error ? " err" : "");
      var s2 = document.createElement("summary");
      s2.textContent = d.is_error ? "error" : "result";
      var pre2 = document.createElement("pre");
      pre2.textContent = d.content || "";
      el.append(s2, pre2);
    } else {
      el = document.createElement("div");
      el.className = "sys";
      el.textContent = d.text || (d.model ? "session " + (d.session_id || "") + " · " + d.model : "");
      if (!el.textContent) return;
    }
    feed.appendChild(el);
    if (stick) feed.scrollTop = feed.scrollHeight;
  }

  var graphTimer = null;
  function scheduleGraph() {
    if (page !== "run") return;
    clearTimeout(graphTimer);
    graphTimer = setTimeout(function () {
      var box = document.getElementById("graph");
      if (box) renderGraph(box);
    }, 400);
  }

  document.body.addEventListener("htmx:sseOpen", function (e) {
    var src = e.detail.source;
    if (!src || src._shipWired) return;
    src._shipWired = true;
    src.addEventListener("output", function (ev) { onOutput(JSON.parse(ev.data)); });
    src.addEventListener("agent", function (ev) { onAgent(JSON.parse(ev.data)); });
    src.addEventListener("inbox", function (ev) {
      var n = JSON.parse(ev.data).count, el = document.getElementById("inbox-count");
      if (el) { el.textContent = n; el.classList.toggle("zero", !n); }
    });
  });

  document.addEventListener("DOMContentLoaded", function () {
    renderGraphs();
    // Open the running visit on first load so the live log is visible.
    var live = document.querySelector("#run-timeline .visit.live");
    if (live) live.open = true;
  });
})();
