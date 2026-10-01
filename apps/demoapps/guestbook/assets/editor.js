// The guestbook's message box: a small rich text editor whose output is
// Markdown. People format with buttons and see the result as they type; what
// leaves the page is the Markdown below, which is also what the log stores.
//
// toMarkdown is the one piece that must be exact, so it is a pure function of
// the editor's DOM and has its own tests (apps/web/test/guestbook-editor.test.ts).
// The server does not trust it: it validates, limits and neutralizes whatever
// arrives.

const BLOCK_TAGS = new Set(["DIV", "P", "UL", "OL", "LI", "BLOCKQUOTE"]);
const SAFE_LINK = /^(https?:|mailto:)/i;

function escapeInline(text) {
  return text.replace(/[\\`*_\[\]<]/g, "\\$&");
}

// A line that starts with something Markdown reads as structure would turn a
// typed "- 5" or "1. thing" into a list. Escape it when the text itself
// starts the line; list and quote prefixes are added after this runs.
function escapeLineStart(line) {
  return line.replace(/^(\d+)([.)])/, "$1\\$2").replace(/^[-+>#]/, "\\$&");
}

function wrap(delimiter, inner) {
  if (inner.trim() === "") return inner;
  const lead = inner.match(/^\s*/)[0];
  const trail = inner.match(/\s*$/)[0];
  const core = inner.slice(lead.length, inner.length - trail.length);
  return `${lead}${delimiter}${core}${delimiter}${trail}`;
}

function codeSpan(text) {
  const flat = text.replace(/\s*\n\s*/g, " ");
  if (flat === "") return "";
  const longest = Math.max(0, ...(flat.match(/`+/g) || []).map((run) => run.length));
  const fence = "`".repeat(longest + 1);
  const pad = flat.startsWith("`") || flat.endsWith("`") ? " " : "";
  return `${fence}${pad}${flat}${pad}${fence}`;
}

// Parentheses and spaces would end the target early, and encodeURIComponent
// leaves parentheses alone.
function linkTarget(href) {
  return href.replace(/[()<>\s]/g, (c) =>
    c.charCodeAt(0) < 128 ? "%" + c.charCodeAt(0).toString(16).toUpperCase().padStart(2, "0") : encodeURIComponent(c),
  );
}

function inline(node) {
  if (node.nodeType === 3) {
    return escapeInline(node.nodeValue.replace(/\u00a0/g, " "));
  }
  if (node.nodeType !== 1) return "";
  const tag = node.tagName;
  const children = () => Array.from(node.childNodes, inline).join("");
  switch (tag) {
    case "BR":
      return "\n";
    case "B":
    case "STRONG":
      return wrap("**", children());
    case "I":
    case "EM":
      return wrap("*", children());
    case "CODE":
      return codeSpan(node.textContent.replace(/\u00a0/g, " "));
    case "A": {
      const href = (node.getAttribute("href") || "").trim();
      const text = children();
      return SAFE_LINK.test(href) && text.trim() !== "" ? `[${text}](${linkTarget(href)})` : text;
    }
    case "IMG":
    case "SCRIPT":
    case "STYLE":
      return "";
    default:
      // A block that ended up inside an inline element (some browsers do this
      // with a bold line): keep it on its own line.
      return BLOCK_TAGS.has(tag) ? `\n${children()}\n` : children();
  }
}

// lines returns the Markdown lines for the children of parent. Plain text and
// inline elements make a line, split at <br>; a block element makes its own.
function lines(parent) {
  const out = [];
  let run = null;
  const flush = () => {
    if (run === null) return;
    // A <br> that ends a block is the browser's placeholder for an empty line.
    const text = run.endsWith("\n") ? run.slice(0, -1) : run;
    for (const line of text.split("\n")) {
      out.push(escapeLineStart(line.trim()));
    }
    run = null;
  };
  for (const child of parent.childNodes) {
    if (child.nodeType === 1 && BLOCK_TAGS.has(child.tagName)) {
      flush();
      out.push(...blockLines(child));
    } else {
      run = (run ?? "") + inline(child);
    }
  }
  flush();
  return out;
}

function blockLines(el) {
  switch (el.tagName) {
    case "UL":
    case "OL": {
      const out = [];
      let n = 0;
      for (const item of el.children) {
        if (item.tagName !== "LI") continue;
        const body = trimBlank(lines(item));
        if (body.length === 0) continue;
        n += 1;
        const marker = el.tagName === "UL" ? "- " : `${n}. `;
        body.forEach((line, i) => {
          out.push(i === 0 ? marker + line : line === "" ? "" : " ".repeat(marker.length) + line);
        });
      }
      return out;
    }
    case "BLOCKQUOTE":
      return trimBlank(lines(el)).map((line) => (line === "" ? ">" : `> ${line}`));
    default:
      return lines(el);
  }
}

function trimBlank(list) {
  let start = 0;
  let end = list.length;
  while (start < end && list[start] === "") start++;
  while (end > start && list[end - 1] === "") end--;
  return list.slice(start, end);
}

// toMarkdown serializes the editor's contents.
export function toMarkdown(root) {
  const out = [];
  for (const line of trimBlank(lines(root))) {
    if (line === "" && out[out.length - 1] === "") continue;
    out.push(line);
  }
  return out.join("\n");
}

export function characters(markdown) {
  return Array.from(markdown).length;
}

function normalizeLink(url) {
  const value = url.trim();
  if (value === "") return "";
  if (SAFE_LINK.test(value)) return value;
  return /^[a-z][a-z0-9+.-]*:/i.test(value) ? "" : `https://${value}`;
}

function toggleCode(editor) {
  const selection = editor.ownerDocument.getSelection();
  if (!selection || selection.rangeCount === 0) return;
  const range = selection.getRangeAt(0);
  const anchor = range.commonAncestorContainer;
  const element = anchor.nodeType === 1 ? anchor : anchor.parentElement;
  const existing = element && element.closest("code");
  if (existing && editor.contains(existing)) {
    existing.replaceWith(editor.ownerDocument.createTextNode(existing.textContent));
    return;
  }
  if (range.collapsed || !editor.contains(anchor)) return;
  const code = editor.ownerDocument.createElement("code");
  const text = range.toString();
  range.deleteContents();
  code.textContent = text;
  range.insertNode(code);
  selection.removeAllRanges();
  const inside = editor.ownerDocument.createRange();
  inside.selectNodeContents(code);
  selection.addRange(inside);
}

const COMMANDS = {
  bold: () => document.execCommand("bold"),
  italic: () => document.execCommand("italic"),
  ul: () => document.execCommand("insertUnorderedList"),
  ol: () => document.execCommand("insertOrderedList"),
  quote: (editor) => {
    const selection = document.getSelection();
    const node = selection && selection.anchorNode;
    const element = node && (node.nodeType === 1 ? node : node.parentElement);
    const inQuote = element && element.closest("blockquote") && editor.contains(element);
    document.execCommand("formatBlock", false, inQuote ? "div" : "blockquote");
  },
  code: (editor) => toggleCode(editor),
  link: (editor) => {
    const selection = document.getSelection();
    if (!selection || selection.isCollapsed || !editor.contains(selection.anchorNode)) {
      return false;
    }
    const url = normalizeLink(window.prompt("Link address") || "");
    if (url) document.execCommand("createLink", false, url);
  },
};

const STATE_COMMANDS = { bold: "bold", italic: "italic", ul: "insertUnorderedList", ol: "insertOrderedList" };

// mountEditor wires an editable element and its toolbar. onChange gets the
// current Markdown after every edit.
export function mountEditor(editor, toolbar, onChange) {
  try {
    document.execCommand("defaultParagraphSeparator", false, "div");
  } catch {
    // Older browsers: Enter then makes a paragraph or a <br>, both handled.
  }
  const changed = () => {
    if (editor.textContent === "" && !editor.querySelector("li")) editor.innerHTML = "";
    onChange(toMarkdown(editor));
  };
  const refreshState = () => {
    for (const button of toolbar.querySelectorAll("[data-cmd]")) {
      const state = STATE_COMMANDS[button.dataset.cmd];
      if (!state) continue;
      let on = false;
      try {
        on = document.activeElement === editor && document.queryCommandState(state);
      } catch {
        on = false;
      }
      button.setAttribute("aria-pressed", String(on));
    }
  };
  editor.addEventListener("input", changed);
  editor.addEventListener("keyup", refreshState);
  editor.addEventListener("mouseup", refreshState);
  editor.addEventListener("focus", refreshState);
  // Paste as text: formatting from a web page or a document would bring its
  // own styles, and the Markdown has only the few kinds this box offers.
  editor.addEventListener("paste", (event) => {
    event.preventDefault();
    const text = (event.clipboardData || window.clipboardData).getData("text/plain");
    document.execCommand("insertText", false, text);
  });
  // Dropped files and images have no place in a message.
  editor.addEventListener("drop", (event) => event.preventDefault());
  // mousedown would move focus to the button and drop the selection.
  toolbar.addEventListener("mousedown", (event) => {
    if (event.target.closest("[data-cmd]")) event.preventDefault();
  });
  toolbar.addEventListener("click", (event) => {
    const button = event.target.closest("[data-cmd]");
    if (!button || button.disabled) return;
    const run = COMMANDS[button.dataset.cmd];
    if (!run) return;
    editor.focus();
    run(editor);
    changed();
    refreshState();
  });
  return {
    clear() {
      editor.innerHTML = "";
      onChange("");
    },
    markdown: () => toMarkdown(editor),
  };
}
