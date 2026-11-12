// Claude vLLM Desktop UI. Talks to the Go backend through the Wails bindings
// (window.go.main.App) and renders claude.exe's stream-json events.
//
// Rule for this file: dynamic text goes in through textContent or
// md.render (which escapes everything). Never assign untrusted strings to
// innerHTML directly.
(function () {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };

  const store = {
    get(k) { try { return localStorage.getItem(k); } catch (e) { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } },
  };

  const state = {
    folder: "",
    sessionReady: false,
    sessionId: "",
    busy: false,
    liveText: [],      // streaming text bubbles awaiting their final assistant event
    liveThinking: null,
    tools: new Map(),  // tool_use_id -> card
    permQueue: [],
  };

  let api; // window.go.main.App

  // ---------- helpers ----------

  function toast(msg) {
    const t = $("toast");
    t.textContent = msg;
    t.hidden = false;
    clearTimeout(toast.timer);
    toast.timer = setTimeout(() => { t.hidden = true; }, 4000);
  }

  function errText(e) {
    return (e && (e.message || e.toString())) || String(e);
  }

  const transcript = $("transcript");
  function nearBottom() {
    return transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight < 120;
  }
  function append(node) {
    const stick = nearBottom();
    $("empty").hidden = true;
    transcript.appendChild(node);
    if (stick) transcript.scrollTop = transcript.scrollHeight;
    return node;
  }
  function scrollIfNear(stick) {
    if (stick) transcript.scrollTop = transcript.scrollHeight;
  }

  function clearTranscript() {
    transcript.querySelectorAll(":scope > :not(#empty)").forEach((n) => n.remove());
    $("empty").hidden = false;
    state.liveText = [];
    state.liveThinking = null;
    state.tools.clear();
  }

  function notice(kind, text, detail) {
    const n = el("div", "notice " + kind, text);
    if (detail) n.appendChild(el("pre", "", detail));
    return append(n);
  }

  function relTime(iso) {
    const d = new Date(iso);
    const s = (Date.now() - d.getTime()) / 1000;
    if (s < 60) return "just now";
    if (s < 3600) return Math.floor(s / 60) + " min ago";
    if (s < 86400) return Math.floor(s / 3600) + " h ago";
    if (s < 7 * 86400) return Math.floor(s / 86400) + " d ago";
    return d.toLocaleDateString();
  }

  function baseName(p) {
    const parts = String(p).split(/[\\/]/).filter(Boolean);
    return parts.length ? parts[parts.length - 1] : p;
  }

  function truncate(s, n) {
    s = String(s == null ? "" : s);
    return s.length > n ? s.slice(0, n) + "\n… (" + (s.length - n) + " more characters)" : s;
  }

  // ---------- busy / composer ----------

  function setBusy(b) {
    state.busy = b;
    $("send").hidden = b;
    $("stop").hidden = !b;
    refreshComposer();
  }

  function refreshComposer() {
    const input = $("input");
    input.disabled = !state.sessionReady;
    input.placeholder = state.sessionReady
      ? (state.busy ? "Working… you can queue the next message" : "Ask Claude to work on " + baseName(state.folder))
      : "Choose a project folder to start";
    $("send").disabled = !state.sessionReady || !input.value.trim();
    $("new-chat").disabled = !state.folder;
  }

  function autoSize() {
    const input = $("input");
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 240) + "px";
  }

  // ---------- rendering: messages ----------

  function userBubble(text) {
    const m = el("div", "msg user");
    m.appendChild(el("div", "bubble", text));
    return append(m);
  }

  function assistantBubble(text, live) {
    const m = el("div", "msg assistant");
    const b = el("div", "bubble" + (live ? " live caret" : ""));
    if (live) b.textContent = text; else b.innerHTML = md.render(text);
    m.appendChild(b);
    append(m);
    return b;
  }

  function finalizeText(text) {
    const b = state.liveText.shift();
    if (b) {
      b.classList.remove("live", "caret");
      b.innerHTML = md.render(text);
    } else if (text.trim()) {
      assistantBubble(text, false);
    }
  }

  function thinkingBlock(text) {
    if (state.liveThinking) {
      state.liveThinking.querySelector("div").textContent = text;
      state.liveThinking = null;
      return;
    }
    if (!text.trim()) return;
    const d = el("details", "thinking");
    d.appendChild(el("summary", "", "Thinking"));
    d.appendChild(el("div", "", text));
    append(d);
  }

  // ---------- rendering: tools ----------

  // Paths inside the project folder are shown relative to it.
  function displayPath(p) {
    if (!p || !state.folder) return p;
    const root = state.folder.replace(/[\\/]+$/, "");
    if (p.length > root.length && p.slice(0, root.length).toLowerCase() === root.toLowerCase() &&
        /[\\/]/.test(p[root.length])) {
      return p.slice(root.length + 1);
    }
    return p;
  }

  function toolSummary(name, input) {
    input = input || {};
    const path = input.file_path || input.notebook_path || input.path;
    const pick = input.command || (path && displayPath(path)) || input.pattern ||
      input.url || input.query || input.description;
    if (pick) return String(pick).split("\n")[0];
    const s = JSON.stringify(input);
    return s === "{}" ? "" : s;
  }

  // Line diff by longest common subsequence; falls back to old/new blocks
  // for inputs too large to diff quickly.
  function lineDiff(a, b) {
    const x = a.split("\n"), y = b.split("\n");
    if (x.length * y.length > 400000) return null;
    const n = x.length, m = y.length;
    const dp = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
    for (let i = n - 1; i >= 0; i--)
      for (let j = m - 1; j >= 0; j--)
        dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    const out = [];
    let i = 0, j = 0;
    while (i < n && j < m) {
      if (x[i] === y[j]) { out.push(["ctx", x[i]]); i++; j++; }
      else if (dp[i + 1][j] >= dp[i][j + 1]) { out.push(["del", x[i]]); i++; }
      else { out.push(["add", y[j]]); j++; }
    }
    while (i < n) out.push(["del", x[i++]]);
    while (j < m) out.push(["add", y[j++]]);
    return out;
  }

  function diffView(oldText, newText) {
    const box = el("div", "diff");
    if (oldText == null) { // a new file: every line is an addition
      String(newText || "").split("\n").forEach((l) => box.appendChild(el("div", "add", l)));
      return box;
    }
    const rows = lineDiff(oldText, newText || "");
    if (!rows) {
      (oldText || "").split("\n").forEach((l) => box.appendChild(el("div", "del", l)));
      (newText || "").split("\n").forEach((l) => box.appendChild(el("div", "add", l)));
      return box;
    }
    // Keep 3 lines of context around changes, collapse the rest.
    const keep = rows.map(() => false);
    rows.forEach((r, k) => {
      if (r[0] !== "ctx") for (let d = -3; d <= 3; d++) if (rows[k + d]) keep[k + d] = true;
    });
    let gap = false;
    rows.forEach((r, k) => {
      if (!keep[k]) { if (!gap) box.appendChild(el("div", "gap", "⋯")); gap = true; return; }
      gap = false;
      box.appendChild(el("div", r[0], r[1]));
    });
    return box;
  }

  function section(label, node) {
    const frag = document.createDocumentFragment();
    frag.appendChild(el("div", "label", label));
    frag.appendChild(node);
    return frag;
  }

  // Renders a tool's input; shared by tool cards and the permission dialog.
  function toolInputView(name, input) {
    input = input || {};
    const wrap = document.createDocumentFragment();
    if (name === "Edit" && "old_string" in input) {
      wrap.appendChild(section(input.replace_all ? "Change (every occurrence)" : "Change",
        diffView(input.old_string, input.new_string)));
    } else if (name === "MultiEdit" && Array.isArray(input.edits)) {
      input.edits.forEach((e, k) =>
        wrap.appendChild(section("Change " + (k + 1) + " of " + input.edits.length, diffView(e.old_string, e.new_string))));
    } else if (name === "Write" && "content" in input) {
      const body = truncate(String(input.content).replace(/\n$/, ""), 20000);
      wrap.appendChild(section("New contents", diffView(null, body)));
    } else if ((name === "Bash" || name === "PowerShell") && input.command) {
      wrap.appendChild(section(input.description || "Command", el("pre", "block", input.command)));
    } else {
      wrap.appendChild(section("Input", el("pre", "block", JSON.stringify(input, null, 2))));
    }
    return wrap;
  }

  function toolCard(block) {
    const d = el("details", "tool");
    const s = el("summary");
    s.appendChild(el("span", "tool-name", block.name));
    s.appendChild(el("span", "tool-sum", toolSummary(block.name, block.input)));
    const status = el("span", "status wait", "running");
    s.appendChild(status);
    d.appendChild(s);
    const body = el("div", "tool-body");
    body.appendChild(toolInputView(block.name, block.input));
    d.appendChild(body);
    // Edits are what people most want to see; open them by default.
    if (["Edit", "MultiEdit", "Write"].includes(block.name)) d.open = true;
    append(d);
    const card = { el: d, body: body, status: status };
    state.tools.set(block.id, card);
    return card;
  }

  function resultText(content) {
    if (typeof content === "string") return content;
    if (Array.isArray(content)) {
      return content.map((c) => (c.type === "text" ? c.text : "[" + c.type + "]")).join("\n");
    }
    return content == null ? "" : JSON.stringify(content, null, 2);
  }

  function toolResult(block) {
    const card = state.tools.get(block.tool_use_id);
    const text = resultText(block.content);
    if (!card) return;
    const stick = nearBottom();
    const denied = block.is_error && /denied|stopped the turn|No answer from the user/i.test(text);
    card.status.textContent = block.is_error ? (denied ? "denied" : "error") : "done";
    card.status.className = "status " + (block.is_error ? "err" : "ok");
    card.body.appendChild(section(block.is_error ? "Error" : "Result",
      el("pre", "block" + (block.is_error ? " err" : ""), truncate(text, 8000))));
    if (block.is_error) card.el.open = true;
    scrollIfNear(stick);
  }

  // ---------- stream-json events ----------

  function onStreamEvent(e) {
    const stick = nearBottom();
    if (e.type === "content_block_start") {
      const cb = e.content_block || {};
      if (cb.type === "text") state.liveText.push(assistantBubble("", true));
      if (cb.type === "thinking") {
        const d = el("details", "thinking");
        d.appendChild(el("summary", "", "Thinking…"));
        d.appendChild(el("div", "", ""));
        state.liveThinking = append(d);
      }
    } else if (e.type === "content_block_delta") {
      const delta = e.delta || {};
      if (delta.type === "text_delta") {
        const b = state.liveText[state.liveText.length - 1];
        if (b) b.textContent += delta.text;
      } else if (delta.type === "thinking_delta" && state.liveThinking) {
        state.liveThinking.querySelector("div").textContent += delta.thinking;
      }
    }
    scrollIfNear(stick);
  }

  function onAssistant(msg) {
    const content = (msg && msg.content) || [];
    for (const block of content) {
      if (block.type === "text") finalizeText(block.text || "");
      else if (block.type === "thinking") thinkingBlock(block.thinking || "");
      else if (block.type === "tool_use") toolCard(block);
    }
  }

  function onUser(msg, replay) {
    const content = msg && msg.content;
    if (typeof content === "string") {
      if (replay) userBubble(content);
      return;
    }
    for (const block of content || []) {
      if (block.type === "tool_result") toolResult(block);
      else if (block.type === "text" && replay && !/^\s*</.test(block.text)) userBubble(block.text);
    }
  }

  function onResult(ev) {
    setBusy(false);
    // Drop streaming bubbles that never received a final message.
    state.liveText.forEach((b) => b.classList.remove("caret"));
    state.liveText = [];
    state.tools.forEach((card) => {
      if (card.status.textContent === "running") {
        card.status.textContent = "no result";
        card.status.className = "status";
      }
    });
    if (ev.is_error || (ev.subtype && ev.subtype !== "success")) {
      const detail = ev.result || (ev.errors || []).join("\n") || ev.subtype;
      notice("error", "The turn ended with an error.", detail);
    }
    refreshHistory();
  }

  function handleEvent(ev) {
    if (!ev || typeof ev !== "object") return;
    switch (ev.type) {
      case "system":
        if (ev.subtype === "init" && ev.session_id) state.sessionId = ev.session_id;
        break;
      case "stream_event":
        onStreamEvent(ev.event || {});
        break;
      case "assistant":
        onAssistant(ev.message);
        break;
      case "user":
        onUser(ev.message, ev.replay);
        break;
      case "result":
        onResult(ev);
        break;
      case "raw":
        notice("info", "claude: " + ev.text);
        break;
    }
  }

  function onExit(info) {
    setBusy(false);
    hidePermission(true);
    if (info.expected) {
      notice("info", "Stopped. Your next message continues this chat.");
    } else if (info.code !== 0) {
      notice("error", "Claude Code exited unexpectedly (code " + info.code + "). Your next message restarts it and resumes this chat.",
        info.stderr ? truncate(info.stderr.trim(), 4000) : "");
    }
  }

  // ---------- permissions ----------

  function showPermission() {
    const req = state.permQueue[0];
    const modal = $("perm-modal");
    if (!req) { modal.hidden = true; return; }
    let input = req.input;
    if (typeof input === "string") { try { input = JSON.parse(input); } catch (e) { /* keep */ } }
    const sum = $("perm-summary");
    sum.textContent = "";
    sum.appendChild(document.createTextNode("Claude wants to run "));
    sum.appendChild(el("b", "", req.tool_name));
    const s = toolSummary(req.tool_name, input);
    if (s) sum.appendChild(document.createTextNode(": " + s));
    const detail = $("perm-detail");
    detail.textContent = "";
    detail.appendChild(toolInputView(req.tool_name, input));
    $("perm-tool-name").textContent = req.tool_name;
    $("perm-queue").textContent = state.permQueue.length > 1 ? (state.permQueue.length - 1) + " more waiting" : "";
    modal.hidden = false;
    $("perm-allow").focus();
  }

  function hidePermission(clearAll) {
    if (clearAll) state.permQueue = [];
    showPermission();
  }

  async function answerPermission(allow, always) {
    const req = state.permQueue.shift();
    showPermission();
    if (!req) return;
    try {
      await api.RespondPermission(req.id, allow, always);
    } catch (e) {
      toast(errText(e));
    }
  }

  // ---------- sessions ----------

  async function startSession(resumeId) {
    if (!state.folder) return;
    clearTranscript();
    state.sessionReady = false;
    state.sessionId = resumeId || "";
    setBusy(false);
    markActiveHistory(resumeId || "");
    const starting = notice("info", "Connecting to vLLM…");
    try {
      const info = await api.StartSession(state.folder, resumeId || "", $("perm-mode").value);
      starting.remove();
      $("chip-model").textContent = info.model;
      $("chip-model").hidden = false;
      $("chip-lane").textContent = info.bridge_mode;
      $("chip-lane").hidden = false;
      if (resumeId) {
        const msgs = await api.LoadTranscript(state.folder, resumeId);
        msgs.forEach(handleEvent);
        transcript.scrollTop = transcript.scrollHeight;
      } else {
        showReady();
      }
      state.sessionReady = true;
      refreshComposer();
      $("input").focus();
    } catch (e) {
      starting.remove();
      notice("error", "Could not start a session.", errText(e));
      refreshComposer();
    }
  }

  function showReady() {
    const empty = $("empty");
    empty.querySelector("h1").textContent = "What should we work on in " + baseName(state.folder) + "?";
    empty.querySelector("p").textContent =
      "Claude Code runs here against " + $("chip-model").textContent +
      " on your vLLM server. Tool calls ask for approval unless you change Permissions above.";
    empty.hidden = false;
  }

  async function send() {
    const input = $("input");
    const text = input.value.trim();
    if (!text || !state.sessionReady) return;
    input.value = "";
    autoSize();
    userBubble(text);
    transcript.scrollTop = transcript.scrollHeight;
    setBusy(true);
    try {
      await api.Send(text);
    } catch (e) {
      setBusy(false);
      notice("error", "Could not send the message.", errText(e));
    }
  }

  async function stop() {
    try { await api.Stop(); } catch (e) { toast(errText(e)); }
  }

  async function refreshHistory() {
    const nav = $("history");
    if (!state.folder) { nav.textContent = ""; return; }
    let list = [];
    try { list = await api.ListSessions(state.folder); } catch (e) { /* show empty */ }
    nav.textContent = "";
    if (!list.length) { nav.appendChild(el("div", "hist-empty", "No chats in this folder yet")); return; }
    for (const s of list) {
      const b = el("button", "hist-item");
      b.dataset.id = s.id;
      b.appendChild(el("span", "hist-title", s.title));
      b.appendChild(el("span", "hist-time", relTime(s.modified)));
      b.title = s.title;
      b.addEventListener("click", () => {
        if (state.busy && !confirm("A turn is still running. Stop it and open this chat?")) return;
        startSession(s.id);
      });
      nav.appendChild(b);
    }
    markActiveHistory(state.sessionId);
  }

  function markActiveHistory(id) {
    document.querySelectorAll(".hist-item").forEach((b) => b.classList.toggle("active", !!id && b.dataset.id === id));
  }

  async function setFolder(folder) {
    // One spelling per folder (8.3 names expanded, drive letter upper-cased),
    // because Claude Code keys its saved sessions on the exact path string.
    try { folder = await api.CanonicalFolder(folder); } catch (e) { /* keep as given */ }
    state.folder = folder;
    store.set("folder", folder);
    $("folder-label").textContent = folder;
    $("folder-btn").title = folder;
    await refreshHistory();
    await startSession("");
  }

  // ---------- settings ----------

  async function openSettings() {
    const modal = $("settings-modal");
    const form = $("settings-form");
    $("doctor-out").hidden = true;
    let cfg;
    try { cfg = await api.GetConfig(); } catch (e) { toast(errText(e)); return; }
    form.vllm_url.value = cfg.vllm_url || "";
    form.api_key.value = "";
    $("key-note").textContent = cfg.has_api_key ? "A key is saved. Leave blank to keep it." : "";
    form.mode.value = cfg.mode || "auto";
    form.log.value = cfg.log || "off";
    form.max_tokens.value = cfg.max_tokens || 8192;
    form.context_window.value = cfg.context_window || 131072;
    setModelOptions(cfg.model ? [cfg.model] : [], cfg.model);
    const problems = [cfg.problem].concat(cfg.warnings || []).filter(Boolean);
    $("settings-problem").textContent = problems.join("\n");
    $("settings-problem").hidden = !problems.length;
    try { $("log-dir").textContent = "Bridge log: " + (await api.LogDir()); } catch (e) { /* optional */ }
    modal.hidden = false;
    form.vllm_url.focus();
  }

  function setModelOptions(models, selected) {
    const sel = $("model-select");
    sel.textContent = "";
    if (!models.length) sel.appendChild(el("option", "", "(fetch models first)")).value = "";
    for (const m of models) {
      const o = el("option", "", m);
      o.value = m;
      if (m === selected) o.selected = true;
      sel.appendChild(o);
    }
  }

  async function fetchModels() {
    const form = $("settings-form");
    const btn = $("refresh-models");
    btn.disabled = true;
    btn.textContent = "Fetching…";
    try {
      const models = await api.ListModels(form.vllm_url.value, form.api_key.value);
      if (!models || !models.length) throw new Error("vLLM reported no models at " + form.vllm_url.value);
      setModelOptions(models, form.model.value);
    } catch (e) {
      toast(errText(e));
    } finally {
      btn.disabled = false;
      btn.textContent = "Fetch models";
    }
  }

  async function saveSettings(ev) {
    ev.preventDefault();
    const form = $("settings-form");
    const cfg = {
      vllm_url: form.vllm_url.value.trim(),
      model: form.model.value,
      api_key: form.api_key.value,
      has_api_key: false,
      clear_api_key: false,
      mode: form.mode.value,
      max_tokens: parseInt(form.max_tokens.value, 10) || 0,
      context_window: parseInt(form.context_window.value, 10) || 0,
      log: form.log.value,
      problem: "",
      warnings: [],
    };
    try {
      await api.SaveConfig(cfg);
      $("settings-modal").hidden = true;
      toast("Settings saved");
      if (state.folder) startSession(state.sessionId);
    } catch (e) {
      $("settings-problem").textContent = errText(e);
      $("settings-problem").hidden = false;
    }
  }

  async function runDoctor() {
    const out = $("doctor-out");
    const btn = $("run-doctor");
    out.hidden = false;
    out.textContent = "Running checks and a tool-calling smoke test (can take up to two minutes on a cold model)…";
    btn.disabled = true;
    try { out.textContent = await api.RunDoctor(); }
    catch (e) { out.textContent = errText(e); }
    finally { btn.disabled = false; }
  }

  // ---------- wiring ----------

  function wire() {
    const input = $("input");
    input.addEventListener("input", () => { autoSize(); refreshComposer(); });
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); send(); }
    });
    $("send").addEventListener("click", send);
    $("stop").addEventListener("click", stop);
    $("new-chat").addEventListener("click", () => startSession(""));
    $("folder-btn").addEventListener("click", async () => {
      try {
        const f = await api.PickFolder(state.folder);
        if (f) await setFolder(f);
      } catch (e) { toast(errText(e)); }
    });
    $("perm-mode").addEventListener("change", () => {
      store.set("permMode", $("perm-mode").value);
      // The mode is fixed per claude.exe process; restart, resuming this chat.
      if (state.sessionReady) startSession(state.sessionId);
    });

    $("perm-allow").addEventListener("click", () => answerPermission(true, false));
    $("perm-always").addEventListener("click", () => answerPermission(true, true));
    $("perm-deny").addEventListener("click", () => answerPermission(false, false));
    document.addEventListener("keydown", (e) => {
      if ($("perm-modal").hidden) {
        if (e.key === "Escape" && !$("settings-modal").hidden) $("settings-modal").hidden = true;
        return;
      }
      if (e.key === "Escape") { e.preventDefault(); answerPermission(false, false); }
      if (e.key === "Enter" && document.activeElement === $("perm-allow")) { e.preventDefault(); answerPermission(true, false); }
    });

    $("settings-btn").addEventListener("click", openSettings);
    $("settings-cancel").addEventListener("click", () => { $("settings-modal").hidden = true; });
    $("settings-form").addEventListener("submit", saveSettings);
    $("refresh-models").addEventListener("click", fetchModels);
    $("run-doctor").addEventListener("click", runDoctor);

    window.runtime.EventsOn("claude:event", handleEvent);
    window.runtime.EventsOn("claude:exit", onExit);
    window.runtime.EventsOn("claude:permission", (req) => {
      state.permQueue.push(req);
      if (state.permQueue.length === 1) showPermission();
      else $("perm-queue").textContent = (state.permQueue.length - 1) + " more waiting";
    });
    window.runtime.EventsOn("claude:permission-cancel", () => hidePermission(true));
  }

  async function init() {
    api = window.go.main.App;
    wire();
    const mode = store.get("permMode");
    if (mode) $("perm-mode").value = mode;
    refreshComposer();

    let cfg = null;
    try { cfg = await api.GetConfig(); } catch (e) { /* settings will show it */ }
    if (!cfg || cfg.problem) {
      await openSettings();
      return;
    }
    let folder = "";
    try { folder = await api.InitialFolder(); } catch (e) { /* none */ }
    folder = folder || store.get("folder");
    if (folder) setFolder(folder);
  }

  // The Wails bindings are injected asynchronously; wait for them.
  (function waitForBindings(tries) {
    if (window.go && window.go.main && window.go.main.App && window.runtime) init();
    else if (tries > 0) setTimeout(() => waitForBindings(tries - 1), 25);
    else document.body.textContent = "The application backend did not load.";
  })(400);
})();
