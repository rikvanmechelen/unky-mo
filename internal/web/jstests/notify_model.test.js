// Tests for static/notify-model.js, run by `node --test`.
const test = require("node:test");
const assert = require("node:assert/strict");
const { urlBase64ToBytes, sameKey, deviceLabel, isIOS } = require("../static/notify-model.js");

test("urlBase64ToBytes decodes base64url with or without padding", () => {
  assert.deepEqual([...urlBase64ToBytes("AQID")], [1, 2, 3]);
  assert.deepEqual([...urlBase64ToBytes("-_8")], [0xfb, 0xff]);
  assert.deepEqual([...urlBase64ToBytes("-_8=")], [0xfb, 0xff]);
  const key = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8";
  const bytes = urlBase64ToBytes(key);
  assert.equal(bytes.length, 65);
  assert.equal(bytes[0], 4);
});

test("sameKey compares a subscription's key with the server's", () => {
  const key = "AQID";
  assert.equal(sameKey(new Uint8Array([1, 2, 3]).buffer, key), true);
  assert.equal(sameKey(new Uint8Array([1, 2, 4]).buffer, key), false);
  assert.equal(sameKey(new Uint8Array([1, 2]).buffer, key), false);
  assert.equal(sameKey(null, key), false);
});

test("deviceLabel names the browser and OS", () => {
  const android = "Mozilla/5.0 (Linux; Android 17; Pixel 10 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36";
  const linux = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36";
  const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1";
  const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0";
  const edge = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0";
  const mac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15";
  assert.equal(deviceLabel(android, false), "Chrome on Android");
  assert.equal(deviceLabel(linux, false), "Chrome on Linux");
  assert.equal(deviceLabel(iphone, false), "Safari on iPhone");
  assert.equal(deviceLabel(iphone, true), "Home Screen app on iPhone");
  assert.equal(deviceLabel(firefox, false), "Firefox on Linux");
  assert.equal(deviceLabel(edge, false), "Edge on Windows");
  assert.equal(deviceLabel(mac, false), "Safari on macOS");
  assert.equal(deviceLabel("curl/8", false), "Browser");
});

test("isIOS spots iPhones and iPadOS's desktop user agent", () => {
  assert.equal(isIOS("Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X)", 5), true);
  assert.equal(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5), true);
  assert.equal(isIOS("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 0), false);
  assert.equal(isIOS("Mozilla/5.0 (Linux; Android 17)", 5), false);
});
