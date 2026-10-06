// Pure helpers for notify.js (no DOM), tested in jstests/notify_model.test.js.

// urlBase64ToBytes decodes a base64url string (the VAPID public key) into
// the Uint8Array PushManager.subscribe takes as applicationServerKey.
function urlBase64ToBytes(s) {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// sameKey reports whether a subscription's applicationServerKey (an
// ArrayBuffer, or null) is the server's current key. A different key means
// the server's VAPID key was recreated and the subscription is useless.
function sameKey(buf, publicKey) {
  if (!buf) return false;
  const a = new Uint8Array(buf);
  const b = urlBase64ToBytes(publicKey);
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

// deviceLabel names this browser for the server's device list and logs:
// "Chrome on Android", "Safari on iPhone", "Firefox on Linux".
function deviceLabel(ua, standalone) {
  const os = /iPhone/.test(ua) ? "iPhone"
    : /iPad/.test(ua) ? "iPad"
    : /Android/.test(ua) ? "Android"
    : /Mac OS X|Macintosh/.test(ua) ? "macOS"
    : /Windows/.test(ua) ? "Windows"
    : /CrOS/.test(ua) ? "ChromeOS"
    : /Linux/.test(ua) ? "Linux"
    : "";
  const browser = /Edg\//.test(ua) ? "Edge"
    : /Firefox\/|FxiOS/.test(ua) ? "Firefox"
    : /Chrome\/|CriOS/.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari"
    : "Browser";
  const name = standalone && (os === "iPhone" || os === "iPad") ? "Home Screen app" : browser;
  return os ? `${name} on ${os}` : name;
}

// isIOS reports an iPhone or iPad, including iPadOS's desktop user agent.
function isIOS(ua, maxTouchPoints) {
  return /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && maxTouchPoints > 1);
}

if (typeof module === "object" && module.exports) {
  module.exports = { urlBase64ToBytes, sameKey, deviceLabel, isIOS };
}
