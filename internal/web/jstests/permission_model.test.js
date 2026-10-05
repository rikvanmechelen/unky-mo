// Tests for static/permission-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { editRows, permissionContent, choiceTone } = require("../static/permission-model.js");

const signs = (rows) => rows.map((r) => r.sign + (r.text === undefined ? "" : r.text)).join("|");

test("editRows keeps shared lines as context around the change", () => {
  assert.equal(signs(editRows("a\nb\nc", "a\nB\nc")), " a|-b|+B| c");
  // More than 3 shared lines fold into a gap.
  assert.equal(signs(editRows("1\n2\n3\n4\n5\nx\n6", "1\n2\n3\n4\n5\ny\n6")), "gap| 3| 4| 5|-x|+y| 6");
  assert.equal(signs(editRows("x\n1\n2\n3\n4", "y\n1\n2\n3\n4")), "-x|+y| 1| 2| 3|gap");
});

test("editRows handles inserts and empty strings", () => {
  assert.equal(signs(editRows("a\nc", "a\nb\nc")), " a|+b| c");
  assert.equal(signs(editRows("", "new")), "+new");
  assert.equal(signs(editRows("gone", "")), "-gone");
  assert.equal(signs(editRows("same", "same")), " same");
});

test("editRows caps long diffs", () => {
  const big = Array.from({ length: 500 }, (_, i) => "l" + i).join("\n");
  const rows = editRows("", big);
  assert.equal(rows.length, 401);
  assert.deepEqual(rows[400], { sign: "more", n: 100 });
});

test("permissionContent per tool", () => {
  assert.deepEqual(permissionContent("Bash", { command: "ls -la", description: "List" }),
    { kind: "command", command: "ls -la", description: "List" });
  const edit = permissionContent("Edit", { file_path: "/r/notes.txt", old_string: "world", new_string: "there" });
  assert.equal(edit.kind, "diff");
  assert.equal(edit.path, "/r/notes.txt");
  assert.equal(signs(edit.rows), "-world|+there");
  const multi = permissionContent("MultiEdit", { file_path: "/r/x", edits: [{ old_string: "a", new_string: "b" }, { old_string: "c", new_string: "d" }] });
  assert.equal(signs(multi.rows), "-a|+b|gap|-c|+d");
  const write = permissionContent("Write", { file_path: "/r/new.txt", content: "fresh\nline" });
  assert.equal(write.create, true);
  assert.equal(signs(write.rows), "+fresh|+line");
  assert.deepEqual(permissionContent("Read", { file_path: "/etc/hostname" }), { kind: "path", path: "/etc/hostname" });
  assert.deepEqual(permissionContent("WebFetch", { url: "https://example.com/", prompt: "Title?" }),
    { kind: "url", url: "https://example.com/", prompt: "Title?" });
  assert.deepEqual(permissionContent("ExitPlanMode", { plan: "# P", planFilePath: "/h/.claude/plans/p.md" }),
    { kind: "plan", plan: "# P", path: "/h/.claude/plans/p.md" });
  const other = permissionContent("mcp__x__do", { a: 1 });
  assert.equal(other.kind, "json");
  assert.equal(other.text, '{\n  "a": 1\n}');
  assert.equal(permissionContent("mcp__x__do", { s: "x".repeat(5000) }).text.length, 4002);
  assert.equal(permissionContent("", { a: 1 }), null);
  // A malformed input still gives something to show.
  assert.deepEqual(permissionContent("Bash", null), { kind: "command", command: "", description: "" });
});

test("choiceTone on the probed labels", () => {
  const cases = {
    "Yes": "allow",
    "Yes, then say done": "allow",
    "Yes, manually approve edits": "allow",
    "Yes, and always allow access to /tmp/x from this project": "lasting",
    "Yes, and switch to auto mode · auto mode handles these prompts for you": "lasting",
    "Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)": "lasting",
    "Yes, and don't ask again for example.com": "lasting",
    "Yes, allow reading from /etc during this session": "lasting",
    "Yes, and use auto mode": "lasting",
    "Tell Claude what to change": "allow",
    "No": "deny",
    "No, and tell Claude what to do differently (esc)": "deny",
  };
  for (const [label, want] of Object.entries(cases)) assert.equal(choiceTone(label), want, label);
});
