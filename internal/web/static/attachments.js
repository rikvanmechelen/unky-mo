// Attachments for the chat composer. A file added by the 📎 button, a paste
// (e.g. a screenshot on the clipboard) or a drop uploads right away to
// /api/sessions/{windowID}/attachments. The server keeps the file and
// returns an id and its kind; /prompt then names the ids. An image's path is
// pasted into Claude's pane, which turns it into "[Image #N]"; any other
// file is named in the prompt as a path for Claude to read.

// The image types Claude Code attaches. The server decides by sniffing the
// content; this only picks the chip shown while the upload runs.
const ATTACHABLE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

// The server's caps (maxImageBytes, maxFileBytes), checked before uploading.
const MAX_IMAGE_BYTES = 20 << 20;
const MAX_FILE_BYTES = 50 << 20;

// An image's id carries its extension; a file's is bare hex.
const isImageAttachmentID = (id) => /\.(png|jpg|gif|webp)$/.test(id || "");

function formatBytes(n) {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1 << 20)).toFixed(1)} MB`;
}

// openImageViewer shows one image full size in a modal. Clicking anywhere or
// pressing Escape closes it.
function openImageViewer(src, alt) {
  const dlg = el("dialog", { class: "image-viewer" }, [el("img", { src, alt: alt || "" })]);
  dlg.addEventListener("click", () => dlg.close());
  dlg.addEventListener("close", () => dlg.remove());
  document.body.appendChild(dlg);
  dlg.showModal();
}

// createAttachments wires the attach button, paste and drop targets and
// renders the chips into strip. getWindowID returns the current session's
// window; onChange fires whenever the set or its upload state changes;
// onError reports an upload failure. Each image belongs to the window it was
// added in: detach and restore move a set between the strip and a session's
// draft, uploads still running included.
function createAttachments({ strip, button, pasteTarget, dropTarget, getWindowID, onChange, onError }) {
  // Each item: {file, name, kind: "image" | "file", url, windowID, id, state: "uploading" | "ready" | "error", ctrl}.
  // An item restored from a stored draft has no file; an image's url is the server's copy.
  let items = [];

  const picker = el("input", { type: "file", multiple: "", hidden: "" });
  button.after(picker);
  button.addEventListener("click", () => picker.click());
  picker.addEventListener("change", () => {
    add(picker.files);
    picker.value = "";
  });

  pasteTarget.addEventListener("paste", (e) => {
    const files = Array.from(e.clipboardData?.files || []);
    if (!files.length) return;
    // A screenshot on the clipboard has no text; an image copied from a
    // page may come with some, which still pastes as usual.
    if (!e.clipboardData.types.includes("text/plain")) e.preventDefault();
    add(files);
  });

  const hasFiles = (e) => e.dataTransfer?.types.includes("Files");
  dropTarget.addEventListener("dragover", (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    dropTarget.classList.add("is-dropping");
  });
  dropTarget.addEventListener("dragleave", (e) => {
    if (!dropTarget.contains(e.relatedTarget)) dropTarget.classList.remove("is-dropping");
  });
  dropTarget.addEventListener("drop", (e) => {
    dropTarget.classList.remove("is-dropping");
    if (!hasFiles(e)) return;
    e.preventDefault();
    add(e.dataTransfer.files);
  });

  function add(fileList) {
    const tooBig = [];
    for (const file of Array.from(fileList || [])) {
      const image = ATTACHABLE_TYPES.includes(file.type);
      if (file.size > (image ? MAX_IMAGE_BYTES : MAX_FILE_BYTES)) {
        tooBig.push(file.name || file.type || "file");
        continue;
      }
      upload({
        file, name: file.name, kind: image ? "image" : "file",
        url: image ? URL.createObjectURL(file) : null, windowID: getWindowID(), id: null, state: "uploading",
      });
    }
    if (tooBig.length) {
      onError(`Can't attach ${tooBig.join(", ")}: images up to ${MAX_IMAGE_BYTES >> 20} MB, other files up to ${MAX_FILE_BYTES >> 20} MB`);
    }
  }

  async function upload(item) {
    items.push(item);
    item.ctrl = new AbortController();
    render();
    try {
      const name = encodeURIComponent(item.name || "");
      const res = await fetch(`/api/sessions/${encodeURIComponent(item.windowID)}/attachments?name=${name}`, {
        method: "POST",
        headers: { "Content-Type": item.file.type || "application/octet-stream" },
        body: item.file,
        signal: item.ctrl.signal,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || `upload failed (${res.status})`);
      item.id = data.id;
      // The server sniffed the content: an image that isn't one is a file
      // (and the other way round, a screenshot without a type).
      if (data.kind === "file" && item.kind === "image") {
        URL.revokeObjectURL(item.url);
        item.url = null;
      } else if (data.kind === "image" && item.kind === "file") {
        item.url = URL.createObjectURL(item.file);
      }
      item.kind = data.kind || item.kind;
      item.state = "ready";
    } catch (err) {
      if (err.name === "AbortError") return;
      item.state = "error";
      item.error = err.message;
      // An image detached into another session's draft shows its error
      // when that session's strip comes back.
      if (items.includes(item)) onError(`${item.name || "image"}: ${err.message}`);
    }
    if (items.includes(item)) render();
  }

  function remove(item) {
    items = items.filter((i) => i !== item);
    drop(item, true);
    render();
  }

  // drop forgets an item; deleteOnServer also removes a stored upload.
  function drop(item, deleteOnServer) {
    item.ctrl?.abort();
    if (item.file && item.url) URL.revokeObjectURL(item.url);
    if (deleteOnServer && item.id) {
      fetch(`/api/sessions/${encodeURIComponent(item.windowID)}/attachments/${encodeURIComponent(item.id)}`, { method: "DELETE" })
        .catch(() => {});
    }
  }

  function render() {
    strip.hidden = items.length === 0;
    strip.replaceChildren(...items.map((item) => {
      if (item.kind === "file") return fileChip(item);
      const name = item.name || "pasted image";
      const img = el("img", { src: item.url, alt: "" });
      // A restored image that has expired on the server is gone for good.
      if (!item.file) {
        img.addEventListener("error", () => {
          if (!items.includes(item)) return;
          items = items.filter((i) => i !== item);
          render();
        });
      }
      const thumb = el("button", { type: "button", class: "attachment__thumb", title: name, "aria-label": `View ${name}` }, [img]);
      thumb.addEventListener("click", () => openImageViewer(item.url, name));
      const removeBtn = el("button", { type: "button", class: "attachment__remove", title: "Remove", "aria-label": `Remove ${name}`, text: "×" });
      removeBtn.addEventListener("click", () => remove(item));
      return el("div", { class: `attachment is-${item.state}`, title: item.error || name }, [thumb, removeBtn]);
    }));
    onChange();
  }

  // A file has no preview: a document glyph, its name and its size.
  function fileChip(item) {
    const name = item.name || "file";
    const removeBtn = el("button", { type: "button", class: "attachment__remove", title: "Remove", "aria-label": `Remove ${name}`, text: "×" });
    removeBtn.addEventListener("click", () => remove(item));
    const meta = item.file ? formatBytes(item.file.size) : "attached";
    return el("div", { class: `attachment attachment--file is-${item.state}`, title: item.error || name }, [
      el("span", { class: "attachment__glyph", "aria-hidden": "true", text: "📄" }),
      el("span", { class: "attachment__info" }, [
        el("span", { class: "attachment__name", text: name }),
        el("span", { class: "attachment__meta", text: meta }),
      ]),
      removeBtn,
    ]);
  }

  return {
    ids: () => items.filter((i) => i.state === "ready").map((i) => i.id),
    items: () => items.slice(),
    pending: () => items.some((i) => i.state === "uploading"),
    failed: () => items.some((i) => i.state === "error"),
    count: () => items.length,
    // clear empties the strip after a send. The files stay on the server:
    // Claude Code may still be reading them, and they expire on their own.
    clear() {
      for (const item of items) drop(item, false);
      items = [];
      render();
    },
    // detach empties the strip but keeps the images (and their uploads)
    // alive, for restore when their session is shown again.
    detach() {
      const out = items;
      items = [];
      render();
      return out;
    },
    restore(list) {
      items = list.slice();
      render();
    },
    // fromStored turns a stored draft's [{id, name}] back into items, an
    // image shown from the server's copy. (A file that has expired since is
    // only found out on send.)
    fromStored(windowID, list) {
      return list.map(({ id, name }) => {
        const image = isImageAttachmentID(id);
        return {
          file: null, name, windowID, id, state: "ready", kind: image ? "image" : "file",
          url: image ? authURL(`/api/sessions/${encodeURIComponent(windowID)}/attachments/${encodeURIComponent(id)}`) : null,
        };
      });
    },
    // discard forgets detached images whose prompt was sent elsewhere.
    discard(list) {
      for (const item of list) drop(item, false);
    },
  };
}
