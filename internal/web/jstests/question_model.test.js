// Tests for static/question-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { splitAnswerList, questionAnswers } = require("../static/question-model.js");

test("splitAnswerList undoes Claude Code's join", () => {
  assert.deepEqual(splitAnswerList("Email, Slack"), ["Email", "Slack"]);
  assert.deepEqual(splitAnswerList("Apple"), ["Apple"]);
  assert.deepEqual(splitAnswerList('Apple, Cherry, "Mango, kiwi"'), ["Apple", "Cherry", "Mango, kiwi"]);
  assert.deepEqual(splitAnswerList('"say \\"hi\\"", B'), ['say "hi"', "B"]);
  assert.deepEqual(splitAnswerList("Banana, Mango; kiwi"), ["Banana", "Mango; kiwi"]);
  for (const bad of ["", 'a "b"', '"open', "a, ", '"x"y']) assert.equal(splitAnswerList(bad), null, bad);
});

test("questionAnswers marks picks and typed answers", () => {
  const input = { questions: [
    { question: "Where?", header: "Channel", multiSelect: true, options: [{ label: "Email" }, { label: "Slack" }, { label: "Chat, webhook" }] },
    { question: "How?", header: "Provisioning", options: [{ label: "Script", description: "d" }, { label: "Terraform" }] },
    { question: "Env?", options: [{ label: "Prod" }] },
    { question: "Skipped?", options: [{ label: "X" }] },
  ] };
  const result = {
    answers: { "Where?": '"Chat, webhook", Email, Teams', "How?": "Terraform", "Env?": "Prod and staging, please" },
    annotations: { "How?": { notes: "keep it small" } },
  };
  const got = questionAnswers(input, result);
  assert.deepEqual(got[0].options.map((o) => o.picked), [true, false, true]);
  assert.deepEqual(got[0].typed, ["Teams"]);
  assert.deepEqual(got[1].options.map((o) => o.picked), [false, true]);
  assert.equal(got[1].options[0].description, "d");
  assert.equal(got[1].notes, "keep it small");
  // Single-select answers are never split.
  assert.deepEqual(got[2].typed, ["Prod and staging, please"]);
  assert.equal(got[3].answered, false);
  assert.deepEqual(got[3].typed, []);
  assert.equal(questionAnswers(input, { questions: [] }), null);
  assert.equal(questionAnswers(input, "Your questions have been answered…"), null);
});
