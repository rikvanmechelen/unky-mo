# File attachments

The composer could attach only PNG, JPEG, GIF and WebP images. Images
already upload their bytes (any device works), and `/prompt` pastes each
stored file's path into Claude's pane, which turns it into `[Image #N]`.
This plan adds every other kind of file: a PDF, a log, a CSV, a zip.

Claude Code has no attachment for those, but it can `Read` a path. So a
non-image file is stored the same way and named in the prompt's text as a
path on the machine running mo.

## Decisions

- **The content decides, not the name.** The server still sniffs the first
  512 bytes: one of the four image types is an image (ignore its name, as
  today); anything else is a file. A `.png` that isn't a PNG is a file.
- **Keep the name for files.** Claude reads `report.pdf` better than a
  random id. A file is stored as `<windowID>/<32 hex>/<safe name>`, and its
  id is the 32 hex (images keep `<32 hex>.<ext>`). The safe name is the
  base name with everything outside `[A-Za-z0-9._+-]` replaced by `_`
  (no spaces, quotes or control characters in a path that is typed into a
  pane), no leading dot, at most 100 bytes keeping the extension, `file`
  when nothing is left.
- **Caps:** images stay at 20 MB, other files 50 MB. Empty uploads are
  refused. The body streams to disk instead of being read into memory.
- **Never served back.** `GET …/attachments/{id}` stays for images only
  (draft thumbnails). A file id is a 404 there: the server would otherwise
  hand any uploaded content back to the browser under its origin.
- **In the prompt:** image paths still go through `SendPrompt`. File paths
  are added after the text, `Attached file: <path>` (or an `Attached files:`
  list), so the message goes as one paste, or as literal text when it's one
  line.
- **Expiry and drafts** stay as they are (24 h, `Sweep` removes a file's
  directory like an image file). A stored draft's `{id, name}` tells the
  kind from the id's form.

## Step 1 in detail: server

- `attachments.go`:
  - `Save(windowID, name, r)` returns `(Attachment{ID, Kind, Name}, error)`.
    Sniff the head, choose image or file, stream the rest under a limit
    (`maxImageBytes` / `maxFileBytes`), remove a partial file on any error.
  - `Path` accepts both id forms. A file id resolves to the one regular file
    in its directory, whose name must pass the safe-name pattern (Lstat, so
    no symlinks).
  - `Delete` removes a file's directory too. `isImageAttachmentID(id)`.
  - Errors: `ErrAttachmentTooLarge` (names both caps), `ErrEmptyAttachment`;
    `ErrUnsupportedAttachment` goes away.
- `handlers_attachments.go`: the name comes from `?name=`; the response is
  `{id, kind, name}`; the body limit is `maxFileBytes+1`; an empty body is a
  400; `GET` refuses non-image ids.
- `handlers_prompt.go`: split the resolved paths by kind, add the file lines
  to the text (`withFileLines`), then send as before. The limit message says
  "attachments".
- Tests: `attachments_test.go` (files stored under their safe name, the
  name cleaning cases, a non-image under an image name is a file, empty,
  both caps, `Path` refusing a symlink or a second file in a file's
  directory, `Delete` and `Sweep` removing the directory),
  `handlers_prompt_test.go` (file only, file + text, image + file),
  a handler test for upload's response and `GET` refusing a file id.

## Step 2 in detail: browser

- `attachments.js`: accept any file from the picker, paste and drop; upload
  with `?name=` and the file's type (or `application/octet-stream`); check
  the 50 MB cap before uploading; keep the server's `kind`. An image chip
  stays a thumbnail; a file chip shows a document glyph, the name and the
  size, and isn't clickable. `fromStored` gets the kind from the id.
- `chat.html`/`chat.js`: wording ("Attach files", "Still uploading…").
- `style.css`: `.attachment--file`, a wider chip with the name cut off.
- Check the page headless (upload a text file, send, see the path in the
  prompt) before calling it done.
