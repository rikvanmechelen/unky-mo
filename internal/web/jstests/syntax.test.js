// Every page script is a classic script with no build step, so a syntax
// error only shows up as a blank page in the browser. The other tests load
// just the pure model files; this compiles every script under static/ (the
// vendored bundle aside) so a typo anywhere fails `go test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const staticDir = path.join(__dirname, "../static");

for (const file of fs.readdirSync(staticDir).filter((f) => f.endsWith(".js"))) {
  test(`${file} compiles`, () => {
    const code = fs.readFileSync(path.join(staticDir, file), "utf8");
    assert.doesNotThrow(() => new vm.Script(code, { filename: file }));
  });
}
