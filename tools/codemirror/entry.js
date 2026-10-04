// Entry point for internal/web/static/vendor/codemirror.js — bundled into a
// single IIFE that exposes `window.CM`, so the dashboard's classic scripts can
// use CodeMirror without a module loader. Rebuild with `make codemirror`.
//
// Everything is bundled once (no CDN, no importmap): CodeMirror breaks when two
// copies of @codemirror/state are loaded, and the dashboard must work offline.

export { EditorState, StateField, StateEffect, Compartment, RangeSetBuilder } from "@codemirror/state";
export {
  EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter,
  highlightSpecialChars, drawSelection, rectangularSelection, crosshairCursor,
  Decoration, WidgetType, ViewPlugin, gutter, GutterMarker,
} from "@codemirror/view";
export { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
export {
  search, searchKeymap, highlightSelectionMatches, openSearchPanel, gotoLine,
} from "@codemirror/search";
export { closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
export {
  syntaxHighlighting, HighlightStyle, indentOnInput, bracketMatching, foldGutter,
  foldKeymap, StreamLanguage, LanguageSupport,
} from "@codemirror/language";
export { tags } from "@lezer/highlight";
export {
  MergeView, unifiedMergeView, getChunks, acceptChunk, rejectChunk,
  goToNextChunk, goToPreviousChunk, getOriginalDoc,
} from "@codemirror/merge";

import { StreamLanguage } from "@codemirror/language";
import { go } from "@codemirror/lang-go";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { css } from "@codemirror/lang-css";
import { html } from "@codemirror/lang-html";
import { markdown } from "@codemirror/lang-markdown";
import { python } from "@codemirror/lang-python";
import { sql } from "@codemirror/lang-sql";
import { yaml } from "@codemirror/lang-yaml";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { toml } from "@codemirror/legacy-modes/mode/toml";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { ruby } from "@codemirror/legacy-modes/mode/ruby";
import { diff } from "@codemirror/legacy-modes/mode/diff";
import { properties } from "@codemirror/legacy-modes/mode/properties";

const stream = (mode) => () => StreamLanguage.define(mode);

const byExt = {
  go: go,
  js: javascript, mjs: javascript, cjs: javascript,
  jsx: () => javascript({ jsx: true }),
  ts: () => javascript({ typescript: true }),
  tsx: () => javascript({ typescript: true, jsx: true }),
  json: json, jsonl: json,
  css: css, scss: css,
  html: html, htm: html, tmpl: html,
  md: markdown, markdown: markdown,
  py: python,
  sql: sql,
  yml: yaml, yaml: yaml,
  sh: stream(shell), bash: stream(shell), zsh: stream(shell), fish: stream(shell),
  toml: stream(toml),
  rb: stream(ruby),
  diff: stream(diff), patch: stream(diff),
  ini: stream(properties), env: stream(properties), properties: stream(properties),
};

const byName = {
  dockerfile: stream(dockerFile),
  makefile: stream(shell),
  gemfile: stream(ruby),
  rakefile: stream(ruby),
};

// languageFor returns a language extension for a file path, or null when the
// file type isn't known (plain text).
export function languageFor(path) {
  const base = (path.split("/").pop() || "").toLowerCase();
  const named = byName[base] || (base.startsWith("dockerfile") && byName.dockerfile);
  if (named) return named();
  const dot = base.lastIndexOf(".");
  const ext = dot >= 0 ? base.slice(dot + 1) : "";
  const make = byExt[ext];
  return make ? make() : null;
}
