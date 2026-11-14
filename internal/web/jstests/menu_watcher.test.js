// Tests for createMenuWatcher (static/menu.js), run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createMenuWatcher } = require("../static/menu.js");

// fakeNet hands out requests the test answers by hand, and timers it runs
// by hand.
function fakeNet() {
  const requests = [];
  const timers = [];
  const deps = {
    fetch(url, opts = {}) {
      return new Promise((resolve) => requests.push({ url, opts, method: opts.method || "GET", resolve }));
    },
    setTimeout(fn) { timers.push(fn); return timers.length; },
    clearTimeout(id) { if (id) timers[id - 1] = null; },
  };
  const reply = (status, body) => ({ ok: status >= 200 && status < 300, status, json: async () => body });
  const flush = () => new Promise((r) => setImmediate(r));
  const runTimers = async () => {
    const fns = timers.splice(0).filter(Boolean);
    for (const fn of fns) fn();
    await flush();
  };
  return { deps, requests, timers, reply, flush, runTimers };
}

const menu = (sig) => ({ sig, title: "Select model", rows: [] });

test("polling keeps going when an answer lands during a key press", async () => {
  const net = fakeNet();
  const seen = [];
  const w = createMenuWatcher((m) => seen.push(m && m.sig), () => {}, net.deps);
  w.setWindow("@2");
  w.setActive(true);
  await net.flush();
  net.requests[0].resolve(net.reply(200, { menu: menu("a") }));
  await net.flush();
  await net.runTimers(); // next poll goes out
  assert.equal(net.requests.length, 2);

  const sent = w.send({ keys: [{ key: "down" }] });
  await net.flush();
  assert.equal(net.requests[2].method, "POST");
  // The poll answers while the key press is still out: dropped, but the
  // loop must go on.
  net.requests[1].resolve(net.reply(200, { menu: menu("a") }));
  await net.flush();
  net.requests[2].resolve(net.reply(200, { menu: menu("b") }));
  await sent;
  assert.deepEqual(seen, ["a", "b"]);
  await net.runTimers();
  assert.equal(net.requests.length, 4, "a poll should follow");
  net.requests[3].resolve(net.reply(200, { menu: null }));
  await net.flush();
  assert.deepEqual(seen, ["a", "b", null]);
});

test("an unchanged screen (304) keeps the menu and keeps polling", async () => {
  const net = fakeNet();
  const seen = [];
  const w = createMenuWatcher((m) => seen.push(m && m.sig), () => {}, net.deps);
  w.setWindow("@2");
  w.setActive(true);
  await net.flush();
  net.requests[0].resolve(net.reply(200, { menu: menu("a") }));
  await net.flush();
  await net.runTimers();
  assert.equal(net.requests[1].opts.headers["If-None-Match"], '"a"');
  net.requests[1].resolve(net.reply(304, null));
  await net.flush();
  await net.runTimers();
  assert.equal(net.requests.length, 3);
  assert.deepEqual(seen, ["a"]);
});

test("going inactive drops the menu and stops polling", async () => {
  const net = fakeNet();
  const seen = [];
  const w = createMenuWatcher((m) => seen.push(m && m.sig), () => {}, net.deps);
  w.setWindow("@2");
  w.setActive(true);
  await net.flush();
  net.requests[0].resolve(net.reply(200, { menu: menu("a") }));
  await net.flush();
  w.setActive(false);
  assert.deepEqual(seen, ["a", null]);
  await net.runTimers();
  assert.equal(net.requests.length, 1);
});

test("a refused key press adopts the screen the server saw", async () => {
  const net = fakeNet();
  const seen = [], notes = [];
  const w = createMenuWatcher((m) => seen.push(m && m.sig), (n) => notes.push(n), net.deps);
  w.setWindow("@2");
  w.setActive(true);
  await net.flush();
  net.requests[0].resolve(net.reply(200, { menu: menu("a") }));
  await net.flush();
  const sent = w.send({ keys: [{ key: "enter" }] });
  await net.flush();
  assert.equal(JSON.parse(net.requests[1].opts.body).sig, "a");
  net.requests[1].resolve(net.reply(409, { error: "the menu changed", menu: menu("c") }));
  await sent;
  assert.deepEqual(seen, ["a", "c"]);
  assert.deepEqual(notes, ["the menu changed"]);
});
