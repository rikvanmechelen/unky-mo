// Claude Code's own spinner line ("✻ Booping… (1m 9s · ↓ 3.3k tokens)")
// above the chat view's composer, copied from Claude's pane via
// /api/sessions/{windowID}/spinner (see spinner.go). A snapshot arrives
// once a second; in between, the glyph cycles, the verb shimmers, the
// elapsed time ticks and the token count tweens toward its latest value,
// so it moves like the terminal's rather than in 1s jumps.

const SPINNER_POLL_MS = 1000;
const SPINNER_FRAMES = ["·", "✢", "✳", "✶", "✻", "✽"];
// Claude Code bounces through its frames rather than wrapping around.
const SPINNER_CYCLE = [...SPINNER_FRAMES, ...SPINNER_FRAMES.slice(1, -1).reverse()];
const SPINNER_FRAME_MS = 120;
const TOKEN_TWEEN_MS = 900;
const DURATION_PART = /^(?=\d)(?:\d+h\s*)?(?:\d+m\s*)?(?:\d+s)?$/;
const TOKENS_PART = /^([↓↑]?\s*)([\d.]+)(k?)(\s*tokens?)$/;

// formatElapsed renders seconds the way the spinner does: "9s", "1m 9s".
function formatElapsed(secs) {
  const h = Math.floor(secs / 3600), m = Math.floor((secs % 3600) / 60), s = secs % 60;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m ${s}s`;
  return `${s}s`;
}

// parseTokens splits "↓ 3.3k tokens" into a number and how to print it
// back; null if the part doesn't look like that.
function parseTokens(part) {
  const m = TOKENS_PART.exec(part || "");
  if (!m) return null;
  const decimals = m[2].includes(".") ? m[2].split(".")[1].length : 0;
  return { value: parseFloat(m[2]) * (m[3] ? 1000 : 1), prefix: m[1], suffix: m[4], decimals: m[3] ? decimals : 1 };
}

function formatTokens(value, t) {
  const n = value >= 1000 ? `${(value / 1000).toFixed(t.decimals)}k` : String(Math.round(value));
  return t.prefix + n + t.suffix;
}

function createSpinner(root) {
  const glyph = el("span", { class: "chat-spinner__glyph", "aria-hidden": "true" });
  const verb = el("span", { class: "chat-spinner__verb" });
  const detail = el("span", { class: "chat-spinner__detail" });
  root.append(glyph, verb, detail);
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  let windowID = null;
  let active = false;
  let gen = 0; // bumped on every stop/switch so a late poll is dropped
  let timer = null;
  let raf = null;

  // The latest snapshot plus what's needed to animate from it.
  let snap = null; // {parts, elapsedIdx, tokensIdx}
  let elapsedBase = 0, elapsedAt = 0, elapsedShown = -1;
  let tokens = null; // {from, to, at, fmt}

  function hide() {
    root.hidden = true;
    snap = null;
    tokens = null;
    elapsedShown = -1;
    if (raf) { cancelAnimationFrame(raf); raf = null; }
  }

  function show(s) {
    if (!s) { hide(); return; }
    const parts = s.parts || [];
    const elapsedIdx = s.elapsed_seconds >= 0 ? parts.findIndex((p) => DURATION_PART.test(p)) : -1;
    const tokensIdx = s.tokens ? parts.indexOf(s.tokens) : -1;
    const now = performance.now();

    if (elapsedIdx >= 0) {
      // Stay monotonic across polls (the local tick can run a little ahead
      // of the snapshot) unless the snapshot jumps well back: a new turn.
      const local = elapsedShown < 0 ? -1 : elapsedShown;
      if (s.elapsed_seconds >= local || s.elapsed_seconds < local - 3) {
        elapsedBase = s.elapsed_seconds;
        elapsedAt = now;
      }
    }

    const t = tokensIdx >= 0 ? parseTokens(s.tokens) : null;
    if (t) {
      const current = tokens ? tokenValue(now) : t.value;
      tokens = { from: current > t.value ? t.value : current, to: t.value, at: now, fmt: t };
    } else {
      tokens = null;
    }

    if (verb.textContent !== s.verb) verb.textContent = s.verb;
    snap = { parts, elapsedIdx, tokensIdx };
    root.hidden = false;
    if (!raf) raf = requestAnimationFrame(frame);
  }

  function tokenValue(now) {
    const p = Math.min(1, (now - tokens.at) / TOKEN_TWEEN_MS);
    const ease = 1 - Math.pow(1 - p, 3);
    return tokens.from + (tokens.to - tokens.from) * ease;
  }

  function frame(now) {
    raf = null;
    if (!snap) return;
    const g = reduceMotion.matches ? "✻" : SPINNER_CYCLE[Math.floor(now / SPINNER_FRAME_MS) % SPINNER_CYCLE.length];
    if (glyph.textContent !== g) glyph.textContent = g;

    const parts = snap.parts.slice();
    if (snap.elapsedIdx >= 0) {
      elapsedShown = Math.max(elapsedShown, elapsedBase + Math.floor((now - elapsedAt) / 1000));
      parts[snap.elapsedIdx] = formatElapsed(elapsedShown);
    }
    if (snap.tokensIdx >= 0 && tokens) parts[snap.tokensIdx] = formatTokens(tokenValue(now), tokens.fmt);
    const text = parts.length ? `(${parts.join(" · ")})` : "";
    if (detail.textContent !== text) detail.textContent = text;

    raf = requestAnimationFrame(frame);
  }

  async function poll() {
    const g = gen;
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/spinner`);
      const data = res.ok ? await res.json() : { spinner: null };
      if (g !== gen) return;
      show(data.spinner);
    } catch (err) {
      // transient — keep the last snapshot animating
    }
    if (g === gen) timer = setTimeout(poll, SPINNER_POLL_MS);
  }

  function stop() {
    active = false;
    gen++;
    clearTimeout(timer);
    timer = null;
    hide();
  }

  return {
    // setActive is driven by the chat view's state poll: the spinner is
    // only looked for while the session is working.
    setActive(on) {
      if (on && !active && windowID) {
        active = true;
        poll();
      } else if (!on && active) {
        stop();
      }
    },
    setWindow(id) {
      stop();
      windowID = id;
    },
  };
}
