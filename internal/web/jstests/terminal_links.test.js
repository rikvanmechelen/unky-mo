// Tests for termLinks in static/terminal.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { termLinks } = require("../static/terminal.js");

const urls = (text) => termLinks(text).map((l) => l.url);

test("http and https URLs are found with their offsets", () => {
  const text = "open https://example.com/a?b=1#c or http://localhost:8080/x now";
  assert.deepEqual(termLinks(text), [
    { start: 5, end: 32, url: "https://example.com/a?b=1#c" },
    { start: 36, end: 59, url: "http://localhost:8080/x" },
  ]);
});

test("trailing punctuation and unopened brackets are left off", () => {
  assert.deepEqual(urls("see https://example.com/x."), ["https://example.com/x"]);
  assert.deepEqual(urls("(https://example.com/x)"), ["https://example.com/x"]);
  assert.deepEqual(urls("https://en.wikipedia.org/wiki/Go_(game))."), ["https://en.wikipedia.org/wiki/Go_(game)"]);
  assert.deepEqual(urls("[link](https://example.com/y)"), ["https://example.com/y"]);
  assert.deepEqual(urls("at https://example.com/z:"), ["https://example.com/z"]);
});

test("quotes, angle brackets and box borders end a URL", () => {
  assert.deepEqual(urls('href="https://example.com/q"'), ["https://example.com/q"]);
  assert.deepEqual(urls("<https://example.com/r>"), ["https://example.com/r"]);
  assert.deepEqual(urls("│ https://example.com/s│"), ["https://example.com/s"]);
  assert.deepEqual(urls("`https://example.com/t`"), ["https://example.com/t"]);
});

test("other schemes and bare prefixes are not links", () => {
  assert.deepEqual(urls("javascript:alert(1) file:///etc/passwd ftp://x.org"), []);
  assert.deepEqual(urls("https:// and http://"), []);
  assert.deepEqual(urls("xhttps://example.com"), []);
});
