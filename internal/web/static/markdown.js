// Small markdown renderer for assistant replies in the chat view. It builds
// DOM nodes directly — transcript text never goes through innerHTML — and
// covers what Claude's replies actually use: paragraphs, headings, fenced
// code, nested lists, quotes, rules, pipe tables, and inline code / bold /
// italic / links. Anything it doesn't recognise stays literal text.
// Uses el() from common.js.

const MD_SAFE_URL = /^(https?:|mailto:)/i;

// renderInline appends text with inline markup to parent.
function renderInline(parent, text) {
  // Order matters: code spans first so their contents stay literal.
  // Emphasis must open before a word character and close after something
  // other than a slash or space, so globs like internal/ops/*.go … cmd/*
  // stay literal.
  const re = /(`+)([\s\S]*?[^`])\1(?!`)|\*\*([^*\n]+?)\*\*|__([^_\n]+?)__|(?<![\w*/])\*([\w"'(\[][^*\n]*?)(?<![\s/])\*(?![\w*])|(?<![\w_])_([\w"'(\[][^_\n]*?)(?<![\s/])_(?![\w_])|\[([^\]\n]+)\]\(([^)\s]+)\)/g;
  let last = 0, m;
  while ((m = re.exec(text))) {
    if (m.index > last) parent.appendChild(document.createTextNode(text.slice(last, m.index)));
    if (m[1]) parent.appendChild(el("code", { text: m[2] }));
    else if (m[3] || m[4]) renderInline(parent.appendChild(el("strong")), m[3] || m[4]);
    else if (m[5] || m[6]) renderInline(parent.appendChild(el("em")), m[5] || m[6]);
    else if (m[7]) {
      if (MD_SAFE_URL.test(m[8])) {
        const a = el("a", { href: m[8], target: "_blank", rel: "noopener noreferrer" });
        renderInline(a, m[7]);
        parent.appendChild(a);
      } else {
        parent.appendChild(document.createTextNode(m[0]));
      }
    }
    last = re.lastIndex;
  }
  if (last < text.length) parent.appendChild(document.createTextNode(text.slice(last)));
  return parent;
}

const MD_FENCE = /^\s*(```+|~~~+)\s*([\w+-]*)\s*$/;
const MD_HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const MD_LIST = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
const MD_RULE = /^\s*([-*_])(\s*\1){2,}\s*$/;
const MD_QUOTE = /^\s*>\s?(.*)$/;
const MD_TABLE_SEP = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;

function splitRow(line) {
  let s = line.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|")) s = s.slice(0, -1);
  return s.split("|").map((c) => c.trim());
}

function isBlockStart(lines, i) {
  const l = lines[i];
  return MD_FENCE.test(l) || MD_HEADING.test(l) || MD_LIST.test(l) || MD_RULE.test(l) || MD_QUOTE.test(l) ||
    (l.includes("|") && i + 1 < lines.length && MD_TABLE_SEP.test(lines[i + 1]));
}

// renderList consumes list items starting at lines[i] (all at indent >=
// baseIndent) and returns [element, nextIndex]. Deeper-indented items nest
// inside the previous item; continuation lines join the item's text.
function renderList(lines, i, baseIndent) {
  const first = lines[i].match(MD_LIST);
  const ordered = /\d/.test(first[2]);
  const list = el(ordered ? "ol" : "ul");
  if (ordered && parseInt(first[2], 10) !== 1) list.setAttribute("start", String(parseInt(first[2], 10)));
  let item = null;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      // A blank line ends the list unless more items follow at this level.
      const next = lines[i + 1];
      if (next !== undefined && MD_LIST.test(next) && next.match(MD_LIST)[1].length >= baseIndent) { i++; continue; }
      break;
    }
    const m = line.match(MD_LIST);
    if (m) {
      const indent = m[1].length;
      if (indent < baseIndent) break;
      if (indent > baseIndent && item) {
        const [sub, ni] = renderList(lines, i, indent);
        item.appendChild(sub);
        i = ni;
        continue;
      }
      if (/\d/.test(m[2]) !== ordered) break;
      item = el("li");
      renderInline(item, m[3]);
      list.appendChild(item);
      i++;
      continue;
    }
    if (item && /^\s+\S/.test(line) && !isBlockStart(lines, i)) {
      item.appendChild(document.createTextNode(" "));
      renderInline(item, line.trim());
      i++;
      continue;
    }
    break;
  }
  return [list, i];
}

// renderMarkdown turns markdown text into a DocumentFragment.
function renderMarkdown(text) {
  const frag = document.createDocumentFragment();
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }

    const fence = line.match(MD_FENCE);
    if (fence) {
      const body = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence[1])) body.push(lines[i++]);
      i++; // closing fence (or end of text)
      const code = el("code", { text: body.join("\n") });
      if (fence[2]) code.dataset.lang = fence[2];
      frag.appendChild(el("pre", { class: "md-code" }, [code]));
      continue;
    }

    const h = line.match(MD_HEADING);
    if (h) {
      frag.appendChild(renderInline(el(`h${Math.min(h[1].length + 2, 6)}`, { class: "md-h" }), h[2]));
      i++;
      continue;
    }

    if (MD_RULE.test(line)) { frag.appendChild(el("hr")); i++; continue; }

    if (line.includes("|") && i + 1 < lines.length && MD_TABLE_SEP.test(lines[i + 1])) {
      const head = splitRow(line);
      const table = el("table", { class: "md-table" });
      const tr = el("tr");
      head.forEach((c) => tr.appendChild(renderInline(el("th"), c)));
      table.appendChild(el("thead", {}, [tr]));
      const tbody = el("tbody");
      i += 2;
      while (i < lines.length && lines[i].includes("|") && lines[i].trim()) {
        const row = el("tr");
        splitRow(lines[i]).forEach((c) => row.appendChild(renderInline(el("td"), c)));
        tbody.appendChild(row);
        i++;
      }
      table.appendChild(tbody);
      frag.appendChild(el("div", { class: "md-table-wrap" }, [table]));
      continue;
    }

    if (MD_LIST.test(line)) {
      const [list, ni] = renderList(lines, i, line.match(MD_LIST)[1].length);
      frag.appendChild(list);
      i = ni;
      continue;
    }

    if (MD_QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && MD_QUOTE.test(lines[i])) body.push(lines[i++].match(MD_QUOTE)[1]);
      frag.appendChild(el("blockquote", {}, [renderMarkdown(body.join("\n"))]));
      continue;
    }

    // Paragraph: consecutive plain lines, kept as separate lines (Claude's
    // replies use single newlines deliberately).
    const p = el("p");
    let firstLine = true;
    while (i < lines.length && lines[i].trim() && (firstLine || !isBlockStart(lines, i))) {
      if (!firstLine) p.appendChild(el("br"));
      renderInline(p, lines[i]);
      firstLine = false;
      i++;
    }
    frag.appendChild(p);
  }
  return frag;
}
