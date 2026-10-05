// Tests for static/diffview.js, run by `node --test` (jstest_test.go).
// The CodeMirror bundle is loaded for real, so highlighting goes through
// the same parsers as in the browser.
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

globalThis.window = globalThis;
vm.runInThisContext(fs.readFileSync(path.join(__dirname, "../static/vendor/codemirror.js"), "utf8") + "\nglobalThis.CM = CM;");
const d = require("../static/diffview.js");

const plain = (segs) => segs.map((s) => s.text).join("");
const classOf = (segs, text) => (segs.find((s) => s.text === text) || {}).cls;

test("highlightText splits tokens into lines", () => {
  const hi = d.highlightText('func a() {\n\treturn "x" // done\n}', "main.go");
  assert.equal(hi.length, 3);
  assert.equal(plain(hi[1]), '\treturn "x" // done');
  assert.equal(classOf(hi[0], "func"), "syn-k");
  assert.equal(classOf(hi[1], '"x"'), "syn-s");
  assert.equal(classOf(hi[1], "// done"), "syn-c");
});

test("a token spanning lines is cut at each line", () => {
  const hi = d.highlightText("a = 1\n/* one\ntwo */\nb = 2", "x.js");
  assert.equal(classOf(hi[1], "/* one"), "syn-c");
  assert.equal(classOf(hi[2], "two */"), "syn-c");
  assert.equal(plain(hi[3]), "b = 2");
});

test("unknown languages and missing paths are not highlighted", () => {
  assert.equal(d.highlightText("hello", "notes.unknownext"), null);
  assert.equal(d.highlightText("hello", ""), null);
});

test("over the cap a block is not highlighted", () => {
  assert.equal(d.highlightText("x\n".repeat(d.HIGHLIGHT_MAX_LINES + 1), "a.go"), null);
});

test("highlightHunk reads each side in its own code", () => {
  // The removed line closes a string that only exists on the old side; if
  // both sides were parsed together, the added line would be read as
  // inside that string.
  const lines = ['-x = "a', '-b"', '+y = 2', ' z = 3'];
  const hi = d.highlightHunk(lines, "a.py");
  assert.equal(hi.length, 4);
  assert.deepEqual(hi.map(plain), ['x = "a', 'b"', "y = 2", "z = 3"]);
  assert.equal(classOf(hi[2], "2"), "syn-n");
  assert.equal(classOf(hi[3], "3"), "syn-n");
});

test("highlightHunk without a language is null", () => {
  assert.equal(d.highlightHunk(["+a"], "x.zzz"), null);
});

test("parseUnifiedDiff reads git diff output", () => {
  const out = [
    "commit abc", "Author: x", "", "    message", "",
    "diff --git a/a.go b/a.go", "index 1..2 100644", "--- a/a.go", "+++ b/a.go",
    "@@ -1,3 +1,3 @@ func x", " one", "-two", "+TWO", " three",
    "@@ -10 +10,2 @@", "-ten", "+TEN", "+eleven",
    "diff --git a/new.txt b/new.txt", "new file mode 100644", "--- /dev/null", "+++ b/new.txt",
    "@@ -0,0 +1 @@", "+hello", "\\ No newline at end of file",
    "diff --git a/old.txt b/old.txt", "--- a/old.txt", "+++ /dev/null", "@@ -1 +0,0 @@", "-bye",
    "diff --git a/img.png b/img.png", "Binary files a/img.png and b/img.png differ",
  ].join("\n");
  const files = d.parseUnifiedDiff(out);
  assert.equal(files.length, 3);
  assert.equal(files[0].path, "a.go");
  assert.deepEqual(files[0].hunks.map((h) => [h.oldStart, h.newStart, h.lines]), [
    [1, 1, [" one", "-two", "+TWO", " three"]],
    [10, 10, ["-ten", "+TEN", "+eleven"]],
  ]);
  assert.equal(files[1].path, "new.txt");
  assert.equal(files[1].added, true);
  assert.deepEqual(files[1].hunks[0].lines, ["+hello"]);
  assert.equal(files[2].path, "old.txt");
  assert.equal(files[2].deleted, true);
});

test("parseUnifiedDiff keeps diff-like lines inside a hunk", () => {
  const out = ["--- a/x", "+++ b/x", "@@ -1,2 +1,2 @@", "---- a/y", "++++ b/y", " @@ -1 +1 @@"].join("\n");
  const files = d.parseUnifiedDiff(out);
  assert.equal(files.length, 1);
  assert.deepEqual(files[0].hunks[0].lines, ["---- a/y", "++++ b/y", " @@ -1 +1 @@"]);
});

test("parseUnifiedDiff reads diff -u and renames", () => {
  const files = d.parseUnifiedDiff(["--- old/a.txt\t2026-01-01", "+++ new/a.txt\t2026-01-02", "@@ -1 +1 @@", "-a", "+b"].join("\n"));
  assert.equal(files[0].path, "new/a.txt");
  assert.equal(files[0].oldPath, "old/a.txt");
});

test("parseUnifiedDiff is null for output without hunks", () => {
  assert.equal(d.parseUnifiedDiff("ok  github.com/x 0.1s"), null);
  assert.equal(d.parseUnifiedDiff("@@ not a diff"), null);
  assert.equal(d.parseUnifiedDiff(undefined), null);
});

test("splitCommand separates heredoc bodies", () => {
  const cmd = "cd x && cat > internal/a.go <<'EOF'\npackage a\nEOF\ngo build ./...";
  const parts = d.splitCommand(cmd);
  assert.deepEqual(parts.map((p) => [p.text, p.path, p.body]), [
    ["cd x && cat > internal/a.go <<'EOF'", "command.sh", false],
    ["package a", "internal/a.go", true],
    ["EOF\ngo build ./...", "command.sh", false],
  ]);
  assert.equal(parts[1].write, true);
  assert.equal(parts.map((p) => p.text).join("\n"), cmd);
});

test("splitCommand names interpreters and tee targets", () => {
  const py = d.splitCommand("python3 - <<'EOF'\nprint(1)\nEOF");
  assert.equal(py[1].path, "script.py");
  assert.equal(py[1].write, false);
  const tee = d.splitCommand('tee -a notes.md <<"END" >/dev/null\n# hi\nEND');
  assert.equal(tee[1].path, "notes.md");
  const strip = d.splitCommand("cat <<-EOF > a.sh\n\techo hi\n\tEOF\n");
  assert.equal(strip[1].path, "a.sh");
  assert.equal(strip[1].text, "\techo hi");
});

test("splitCommand leaves unterminated heredocs and plain commands alone", () => {
  assert.deepEqual(d.splitCommand("cat <<EOF\nnever ends").map((p) => p.body), [false]);
  assert.deepEqual(d.splitCommand("go test ./... 2>&1 | tail"), [{ text: "go test ./... 2>&1 | tail", path: "command.sh", body: false }]);
  assert.equal(d.heredocTarget("cat 2>&1").path, null);
});
