// Tests for static/model-name.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { modelDisplayName, modelFamily, modelFromLine, createModelTracker } = require("../static/model-name.js");

const assistant = (model, extra = {}) => ({ type: "assistant", message: { role: "assistant", model, content: [] }, ...extra });
const user = (content) => ({ type: "user", message: { role: "user", content } });

test("model ids read as Claude Code names them", () => {
  assert.equal(modelDisplayName("claude-opus-5-5"), "Opus 5.5");
  assert.equal(modelDisplayName("claude-fable-5"), "Fable 5");
  assert.equal(modelDisplayName("claude-fable-5-1"), "Fable 5.1");
  assert.equal(modelDisplayName("claude-sonnet-4-5-20250929"), "Sonnet 4.5");
  assert.equal(modelDisplayName("claude-3-5-haiku-20241022"), "Haiku 3.5");
  assert.equal(modelDisplayName("claude-opus-4-6[1m]"), "Opus 4.6 (1M context)");
  assert.equal(modelDisplayName("us.anthropic.claude-sonnet-4-5-20250929-v1:0"), "Sonnet 4.5");
  assert.equal(modelDisplayName("some-other-model"), "some-other-model");
  assert.equal(modelDisplayName(""), "");
});

test("the chip's colour goes by family: opus, fable, or other", () => {
  assert.equal(modelFamily({ name: "Opus 5.5" }), "opus");
  assert.equal(modelFamily({ name: "Opus 4.6 (1M context)" }), "opus");
  assert.equal(modelFamily({ name: "Fable 5.1" }), "fable");
  assert.equal(modelFamily({ name: "Sonnet 5.5" }), "other");
  assert.equal(modelFamily({ name: "Haiku 3.5" }), "other");
  assert.equal(modelFamily({ name: "some-opus-thing" }), "other");
  assert.equal(modelFamily(null), "other");
});

test("an assistant line names its model; synthetic and sidechain lines don't", () => {
  assert.deepEqual(modelFromLine(assistant("claude-sonnet-5-5")), { id: "claude-sonnet-5-5", name: "Sonnet 5.5" });
  assert.equal(modelFromLine(assistant("<synthetic>")), null);
  assert.equal(modelFromLine(assistant("claude-haiku-5-5", { isSidechain: true })), null);
  assert.equal(modelFromLine(assistant(undefined)), null);
  assert.equal(modelFromLine({ type: "link", uuid: "x" }), null);
  assert.equal(modelFromLine(null), null);
});

test("/model's output names the new model, escapes and (default) dropped", () => {
  assert.deepEqual(
    modelFromLine(user("<local-command-stdout>Set model to `Opus 5.5 (default)` and saved as your default for new sessions</local-command-stdout>")),
    { id: "", name: "Opus 5.5" });
  assert.deepEqual(
    modelFromLine(user("<local-command-stdout>Set model to `Fable 5.1` and saved · Draws from usage credits\u001b[2m\u001b[22m\n\u001b[2m     Your organization's default</local-command-stdout>")),
    { id: "", name: "Fable 5.1" });
  assert.deepEqual(
    modelFromLine(user([{ type: "text", text: "<local-command-stdout>Kept model as `Sonnet 5.5`</local-command-stdout>" }])),
    { id: "", name: "Sonnet 5.5" });
  // A prompt that merely quotes the phrase isn't command output.
  assert.equal(modelFromLine(user("why does it say Set model to `Haiku 5.5`?")), null);
});

test("the tracker keeps the newest model and resets", () => {
  const t = createModelTracker();
  assert.equal(t.current(), null);
  assert.equal(t.onLine(assistant("claude-sonnet-5-5")), true);
  assert.equal(t.onLine(assistant("claude-sonnet-5-5")), false);
  assert.equal(t.onLine(user("<local-command-stdout>Set model to `Opus 5.5`</local-command-stdout>")), true);
  assert.equal(t.current().name, "Opus 5.5");
  assert.equal(t.onLine(assistant("<synthetic>")), false);
  assert.equal(t.onLine(assistant("claude-opus-5-5")), true);
  assert.deepEqual(t.current(), { id: "claude-opus-5-5", name: "Opus 5.5" });
  t.reset();
  assert.equal(t.current(), null);
});
