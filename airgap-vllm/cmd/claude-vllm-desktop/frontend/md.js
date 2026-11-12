// A deliberately small Markdown renderer for model output.
//
// Everything is HTML-escaped before any formatting is applied, and no raw
// HTML is ever passed through. That is a security property, not a shortcut:
// this page can call RespondPermission, so markup smuggled into model output
// (say, from a prompt-injected file) must never become live elements that
// could approve tool calls by themselves.
(function () {
  "use strict";

  function esc(s) {
    return s
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  // Inline formatting on already-escaped text. Code spans are cut out first so
  // their contents are not formatted.
  function inline(text) {
    const codes = [];
    let s = esc(text).replace(/`([^`]+)`/g, function (_, c) {
      codes.push(c);
      return "\u0000" + (codes.length - 1) + "\u0000";
    });
    s = s
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/__([^_]+)__/g, "<strong>$1</strong>")
      .replace(/(^|[^*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>")
      .replace(/~~([^~]+)~~/g, "<del>$1</del>")
      // Links render as text plus the target; nothing is navigable offline,
      // and a clickable link would open inside the app window.
      .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, '$1 <span class="link-target">($2)</span>');
    return s.replace(/\u0000(\d+)\u0000/g, function (_, i) {
      return "<code>" + codes[Number(i)] + "</code>";
    });
  }

  function render(src) {
    const lines = String(src || "").replace(/\r\n?/g, "\n").split("\n");
    const out = [];
    let para = [];
    let list = null; // {tag, items}

    function flushPara() {
      if (para.length) {
        out.push("<p>" + para.map(inline).join("<br>") + "</p>");
        para = [];
      }
    }
    function flushList() {
      if (list) {
        out.push("<" + list.tag + ">" + list.items.map(function (i) {
          return "<li>" + inline(i) + "</li>";
        }).join("") + "</" + list.tag + ">");
        list = null;
      }
    }
    function flush() { flushPara(); flushList(); }

    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];

      const fence = line.match(/^\s*(```+|~~~+)\s*([\w+-]*)\s*$/);
      if (fence) {
        flush();
        const close = fence[1];
        const body = [];
        i++;
        while (i < lines.length && lines[i].trim().indexOf(close) !== 0) {
          body.push(lines[i]);
          i++;
        }
        const lang = fence[2] ? ' data-lang="' + esc(fence[2]) + '"' : "";
        out.push("<pre class=\"code\"" + lang + "><code>" + esc(body.join("\n")) + "</code></pre>");
        continue;
      }

      if (/^\s*$/.test(line)) { flush(); continue; }

      const h = line.match(/^(#{1,6})\s+(.*)$/);
      if (h) {
        flush();
        const level = Math.min(h[1].length + 2, 6); // keep headings modest
        out.push("<h" + level + ">" + inline(h[2]) + "</h" + level + ">");
        continue;
      }

      if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) { flush(); out.push("<hr>"); continue; }

      const q = line.match(/^\s*>\s?(.*)$/);
      if (q) { flush(); out.push("<blockquote>" + inline(q[1]) + "</blockquote>"); continue; }

      const ul = line.match(/^\s*[-*+]\s+(.*)$/);
      const ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (ul || ol) {
        flushPara();
        const tag = ul ? "ul" : "ol";
        if (!list || list.tag !== tag) { flushList(); list = { tag: tag, items: [] }; }
        list.items.push((ul || ol)[1]);
        continue;
      }

      // Table: header row followed by a |---| separator.
      if (line.indexOf("|") !== -1 && i + 1 < lines.length && /^\s*\|?\s*:?-{2,}/.test(lines[i + 1])) {
        flush();
        const cells = function (l) {
          return l.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map(function (c) { return c.trim(); });
        };
        const head = cells(line);
        i += 2;
        const rows = [];
        while (i < lines.length && lines[i].indexOf("|") !== -1 && lines[i].trim() !== "") {
          rows.push(cells(lines[i]));
          i++;
        }
        i--;
        out.push("<table><thead><tr>" + head.map(function (c) { return "<th>" + inline(c) + "</th>"; }).join("") +
          "</tr></thead><tbody>" + rows.map(function (r) {
            return "<tr>" + r.map(function (c) { return "<td>" + inline(c) + "</td>"; }).join("") + "</tr>";
          }).join("") + "</tbody></table>");
        continue;
      }

      if (list && /^\s{2,}\S/.test(line)) {
        list.items[list.items.length - 1] += " " + line.trim();
        continue;
      }
      flushList();
      para.push(line);
    }
    flush();
    return out.join("\n");
  }

  window.md = { render: render, escape: esc };
})();
