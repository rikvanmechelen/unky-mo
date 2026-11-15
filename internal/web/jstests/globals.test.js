// The pages load their scripts as classic scripts sharing one global scope,
// so a top-level name declared in two of a page's scripts breaks the page:
// a second `const`/`let`/`class` is a SyntaxError that drops the whole
// script, and a second `function` silently replaces the first. Node's other
// tests load one file at a time and can't see that, so this checks every
// page's script list.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const staticDir = path.join(__dirname, "../static");
const DECL = /^(?:async\s+)?(?:function\*?|const|let|var|class)\s+([A-Za-z_$][\w$]*)/gm;

for (const page of fs.readdirSync(staticDir).filter((f) => f.endsWith(".html"))) {
  test(`${page}: no top-level name is declared by two scripts`, () => {
    const html = fs.readFileSync(path.join(staticDir, page), "utf8");
    const scripts = [...html.matchAll(/<script[^>]*\ssrc="\/([^"]+\.js)"/g)].map((m) => m[1]);
    assert.ok(scripts.length, "no scripts found");
    const seen = new Map(); // name -> script
    const clashes = [];
    for (const src of scripts) {
      // The launcher's files are served under /hub/ (hub_core.go's coreFiles).
      const file = src.startsWith("hub/") ? src.slice("hub/".length) : src;
      const code = fs.readFileSync(path.join(staticDir, file), "utf8");
      for (const m of code.matchAll(DECL)) {
        const prev = seen.get(m[1]);
        if (prev && prev !== src) clashes.push(`${m[1]}: ${prev} and ${src}`);
        else seen.set(m[1], src);
      }
    }
    assert.deepEqual(clashes, []);
  });
}
