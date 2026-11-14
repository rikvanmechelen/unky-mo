# Faster session switches in the chat view

Switching session in the chat view replays the session's whole JSONL over SSE
before the transcript shows. On 2026-11-14 that was 59 MB for one session
(`kids_com [app]`) and 10 MB for another (`kids_com [firmware]`): seconds on a
phone, and a blank middle column meanwhile.

## Done (648e0ab)

- `dropToolImages` (`transcript.go`) drops a tool result's images: an image
  `Read` stored each screenshot twice as base64 (`toolUseResult.file.base64`
  and the `tool_result`'s image block), ~600 KB each, and the chat showed
  neither (the block was `JSON.stringify`'d into the Output box). 59 → 7.5 MB.
  The block keeps `source: {type: "omitted", media_type}`; the card says
  "Image (image/png)".
- `transcriptGhost` (`chat.js`): a shimmering skeleton conversation in place
  of the hidden transcript until `caught-up`, fading in after 150 ms.

## Where the time goes now (measured, headless Chrome, localhost)

| switch to | stream | last byte | shown | main thread busy |
|---|---|---|---|---|
| firmware | 10.4 MB | 454 ms | 660 ms | 503 ms |
| app | 7.6 MB | 980 ms | 1149 ms | 378 ms |

Locally the JS is ~0.4–0.5 s; the rest is the stream, and on a remote link
transfer dominates (10 MB at 20 Mbit/s ≈ 4 s). So the next win is bytes.

What firmware's 10.4 MB is: no single big thing. `message.content` 5.9 MB
(thinking-block `signature`s alone 1.5 MB — random base64 the chat never
shows), `toolUseResult` 1.2 MB, `wireToolInputs` 0.85 MB, `usage` 0.9 MB,
`serverClassifierContext` 0.3 MB, the rest per-line envelope fields.

gzip of the forwarded backlog: firmware 10.4 → 3.3 MB (level 1, 40 ms),
app 7.6 → 1.9 MB. The signatures are most of what's left (1.5 MB of firmware's
3.3 MB, since base64 compresses badly).

**Not doing "recent part first"** (send the tail, then the older history): the
Overview's intent trace (`overview.onTranscriptLine`) needs every line, the
active-branch walk needs the parents, and a tool result's card needs its
`tool_use`. That's a two-phase merge and a re-render for what gzip + dropping
signatures gets mostly for free. Revisit if a session's compressed replay is
still slow.

## Step 1 in detail: gzip the stream, drop thinking signatures

- `streamTranscript` (`handlers_transcript.go`, shared by the chat and the
  subagent dialog) wraps the response in a `gzip.Writer` (level 1: the stream
  is generated per request, and level 5 only saves ~10% more) when the request
  accepts gzip (`acceptsGzip`, the helper `static.go` already uses), with
  `Content-Encoding: gzip` and `Vary: Accept-Encoding`. It flushes the gzip
  writer before each `rc.Flush()`: after the backlog + `caught-up`, and after
  every live event, so live lines aren't held in the compressor.
- `dropToolImages` grows into `slimLine` for user *and* assistant lines:
  thinking blocks lose `signature`, redacted thinking blocks lose `data`
  (the same kind of opaque blob). The chat skips thinking blocks
  (`renderMessage`); nothing else reads them (grepped).
- Tests: the cursor test covers an assistant line with a thinking block
  (signature gone, thinking text and other blocks byte-equal after a
  round-trip, a line without one byte for byte). A handler test reads the SSE
  stream through `httptest.NewServer` with `Accept-Encoding: gzip`, checks the
  header, decompresses and finds the backlog + `caught-up`, then appends a
  line to the file and reads the live event through the gzip stream (proves
  the flush). Without the header the stream stays plain.
- Measure the switch again (`timing.mjs`-style CDP probe, with throttling).

Built as planned. Measured after (headless Chrome, same probe):

| switch to | on the wire (gzip) | was | shown at 20 Mbit/s | was |
|---|---|---|---|---|
| firmware | 2.0 MB | 10.4 MB | 1.2 s | 4.7 s |
| app | 1.0 MB | 59 MB | 1.1 s | — |

Locally a switch is now mostly the browser's own work (~0.4–0.6 s of JS)
plus the server re-reading the file.

## Step 2 in detail: screenshots as thumbnails, loaded when a card opens

- `GET /api/sessions/{windowID}/tool-image/{toolUseID}?i=N`
  (`handlers_toolimage.go`): the session comes from the state row like the
  transcript (`resolveSession`), never from the request. `toolUseID` must
  match `^[A-Za-z0-9_-]{1,128}$`, `i` is the image's index among the result's
  image blocks (0–31). The server reads the JSONL, finds the user line whose
  `tool_result` has that `tool_use_id` (a `bytes.Index` for the quoted id
  first, then a real JSON parse of that line, so a mention in other text
  doesn't count), decodes the `i`th image block's base64, sniffs it with the
  same 4-type allowlist as attachments (PNG/JPEG/GIF/WebP; SVG never) and
  serves it with its sniffed type, `X-Content-Type-Options: nosniff` and
  `Cache-Control: private, max-age=86400, immutable` (a tool result never
  changes). 404 for an unknown id or index, 400 for a malformed one.
- `chat.js` `fillToolResult`: an image block with `source.type: "omitted"`
  becomes a thumbnail button (`<img loading="lazy">`, the prompt thumbnails'
  look) that opens `openImageViewer`; the "Image (image/png)" label stays as
  its caption. The URL comes from `opts.toolImageURL(toolUseID, i)`; the
  subagent dialog passes none (its transcripts are other files), so it keeps
  the label. A card's body is hidden while folded, and lazy images inside
  `display: none` aren't fetched, so nothing loads until a card opens.
- Tests: handler test against a temp JSONL (found, second image by index,
  unknown id, index out of range, malformed id, an id that only appears in
  text, an SVG or HTML payload refused, a window without a session → 404 with
  no file read).
