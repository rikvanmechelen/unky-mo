// Service worker: shows mo web's push notifications and opens the session on
// tap (docs/plans/notifications.md). No fetch handler: nothing is cached, the
// dashboard is only ever live.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  // Always show something: iOS revokes a subscription whose pushes show
  // nothing, and Chrome shows its own "updated in the background" instead.
  let msg = {};
  try {
    msg = event.data ? event.data.json() : {};
  } catch {
    msg = {};
  }
  const title = msg.title || "Unky Mo";
  const options = {
    body: msg.body || "",
    icon: "/icons/icon-192.png",
    badge: "/icons/badge.png",
    data: { url: msg.url || "/", window: msg.window || "" },
  };
  if (msg.tag) {
    options.tag = msg.tag;
    options.renotify = true; // a replacing notification buzzes again
  }
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = new URL((event.notification.data && event.notification.data.url) || "/", self.location.origin);
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const same = windows.find((c) => c.url === url.href);
    if (same) return same.focus();
    for (const c of windows) {
      if (new URL(c.url).origin !== url.origin || !("navigate" in c)) continue;
      try {
        const nav = await c.navigate(url.href);
        if (nav) return nav.focus();
      } catch {
        // Not controlled by this worker (opened before it): try the next one.
      }
    }
    return self.clients.openWindow(url.href);
  })());
});
