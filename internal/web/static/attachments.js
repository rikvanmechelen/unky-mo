// Image attachments for the chat composer. An image added by the 📎 button,
// a paste (e.g. a screenshot on the clipboard) or a drop uploads right away
// to /api/sessions/{windowID}/attachments. The server keeps the file and
// returns an id; /prompt then names the ids and the server pastes each
// file's path into Claude's pane, which turns it into "[Image #N]".

// The image types Claude Code attaches (the server sniffs them too).
const ATTACHABLE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

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
// onError reports an upload failure.
function createAttachments({ strip, button, pasteTarget, dropTarget, getWindowID, onChange, onError }) {
  // Each item: {file, url, id, state: "uploading" | "ready" | "error", ctrl, chip}.
  let items = [];

  const picker = el("input", { type: "file", accept: ATTACHABLE_TYPES.join(","), multiple: "", hidden: "" });
  button.after(picker);
  button.addEventListener("click", () => picker.click());
  picker.addEventListener("change", () => {
    add(picker.files);
    picker.value = "";
  });

  pasteTarget.addEventListener("paste", (e) => {
    const files = imageFiles(e.clipboardData?.files);
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

  function imageFiles(list) {
    return Array.from(list || []).filter((f) => f.type.startsWith("image/"));
  }

  function add(fileList) {
    const files = Array.from(fileList || []);
    const rejected = files.filter((f) => !ATTACHABLE_TYPES.includes(f.type));
    if (rejected.length) onError(`Can't attach ${rejected.map((f) => f.name || f.type).join(", ")}: only PNG, JPEG, GIF and WebP images`);
    for (const file of files) {
      if (ATTACHABLE_TYPES.includes(file.type)) upload({ file, url: URL.createObjectURL(file), id: null, state: "uploading" });
    }
  }

  async function upload(item) {
    const windowID = getWindowID();
    items.push(item);
    item.ctrl = new AbortController();
    render();
    try {
      const res = await fetch(`/api/sessions/${encodeURIComponent(windowID)}/attachments`, {
        method: "POST",
        headers: { "Content-Type": item.file.type },
        body: item.file,
        signal: item.ctrl.signal,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || `upload failed (${res.status})`);
      item.id = data.id;
      item.state = "ready";
    } catch (err) {
      if (err.name === "AbortError") return;
      item.state = "error";
      item.error = err.message;
      onError(`${item.file.name || "image"}: ${err.message}`);
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
    URL.revokeObjectURL(item.url);
    if (deleteOnServer && item.id) {
      fetch(`/api/sessions/${encodeURIComponent(getWindowID())}/attachments/${encodeURIComponent(item.id)}`, { method: "DELETE" })
        .catch(() => {});
    }
  }

  function render() {
    strip.hidden = items.length === 0;
    strip.replaceChildren(...items.map((item) => {
      const name = item.file.name || "pasted image";
      const thumb = el("button", { type: "button", class: "attachment__thumb", title: name, "aria-label": `View ${name}` }, [
        el("img", { src: item.url, alt: "" }),
      ]);
      thumb.addEventListener("click", () => openImageViewer(item.url, name));
      const removeBtn = el("button", { type: "button", class: "attachment__remove", title: "Remove", "aria-label": `Remove ${name}`, text: "×" });
      removeBtn.addEventListener("click", () => remove(item));
      return el("div", { class: `attachment is-${item.state}`, title: item.error || name }, [thumb, removeBtn]);
    }));
    onChange();
  }

  return {
    ids: () => items.filter((i) => i.state === "ready").map((i) => i.id),
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
  };
}
