// The guestbook's message box lives with the guestbook (apps/demoapps/guestbook/assets),
// which is plain files with no build step of its own. Its serializer is the one
// piece of front end that has to be exact, so it is tested here, where a DOM is.
import { describe, expect, it } from "vitest";
import { characters, mountEditor, toMarkdown } from "../../demoapps/guestbook/assets/editor.js";

function md(html) {
  const root = document.createElement("div");
  root.innerHTML = html;
  return toMarkdown(root);
}

describe("toMarkdown", () => {
  it("is empty for an empty box", () => {
    expect(md("")).toBe("");
    expect(md("<div><br></div>")).toBe("");
    expect(md("  <br> ")).toBe("");
  });

  it("passes plain text through", () => {
    expect(md("hello there")).toBe("hello there");
  });

  it("formats bold, italic and code", () => {
    expect(md("a <b>bold</b>, <strong>strong</strong>, <i>it</i>, <em>em</em> and <code>code</code>")).toBe(
      "a **bold**, **strong**, *it*, *em* and `code`",
    );
    expect(md("<b><i>both</i></b>")).toBe("***both***");
  });

  it("keeps whitespace outside the delimiters, where Markdown needs it", () => {
    expect(md("a<b> bold </b>b")).toBe("a **bold** b");
    expect(md("<b> </b>x")).toBe("x");
    expect(md("<i>   </i>")).toBe("");
  });

  it("makes links, but only to places a visitor could mean", () => {
    expect(md('<a href="https://poweur.net/a b">site</a>')).toBe("[site](https://poweur.net/a%20b)");
    expect(md('<a href="mailto:a@b.example">mail</a>')).toBe("[mail](mailto:a@b.example)");
    expect(md('<a href="javascript:alert(1)">x</a>')).toBe("x");
    expect(md('<a href="data:text/html,x">x</a>')).toBe("x");
    expect(md('<a href="/relative">x</a>')).toBe("x");
    expect(md('<a href="https://x.example/(1)">p</a>')).toBe("[p](https://x.example/%281%29)");
    expect(md('<a href="https://x.example"><b>bold link</b></a>')).toBe("[**bold link**](https://x.example)");
  });

  it("escapes what Markdown would otherwise read as formatting", () => {
    expect(md("2 * 3 = 6 and snake_case")).toBe("2 \\* 3 = 6 and snake\\_case");
    expect(md("[not a link](x) and `ticks`")).toBe("\\[not a link\\](x) and \\`ticks\\`");
    expect(md("a &lt; b and &lt;b&gt;")).toBe("a \\< b and \\<b>");
    expect(md("back\\slash")).toBe("back\\\\slash");
  });

  it("escapes a line that starts like structure", () => {
    expect(md("- not a list")).toBe("\\- not a list");
    expect(md("+ plus")).toBe("\\+ plus");
    expect(md("1. not numbered")).toBe("1\\. not numbered");
    expect(md("12) not either")).toBe("12\\) not either");
    expect(md("# not a heading")).toBe("\\# not a heading");
    expect(md("> not a quote")).toBe("\\> not a quote");
    expect(md("fine - here, 1. here, # here")).toBe("fine - here, 1. here, # here");
  });

  it("writes code that contains backticks", () => {
    expect(md("<code>a`b</code>")).toBe("``a`b``");
    expect(md("<code>`edge</code>")).toBe("`` `edge ``");
    expect(md("<code>a&lt;b&gt;</code>")).toBe("`a<b>`");
    expect(md("<code></code>x")).toBe("x");
  });

  it("turns each line the browser makes into a line", () => {
    expect(md("one<div>two</div><div>three</div>")).toBe("one\ntwo\nthree");
    expect(md("<div>one</div><div><br></div><div>two</div>")).toBe("one\n\ntwo");
    expect(md("one<br>two")).toBe("one\ntwo");
    expect(md("<p>one</p><p>two</p>")).toBe("one\ntwo");
    expect(md("<div>one<br></div>")).toBe("one");
  });

  it("limits runs of empty lines", () => {
    expect(md("<div>a</div><div><br></div><div><br></div><div><br></div><div>b</div>")).toBe("a\n\nb");
    expect(md("<div><br></div><div>a</div><div><br></div>")).toBe("a");
  });

  it("writes lists", () => {
    expect(md("<ul><li>one</li><li>two</li></ul>")).toBe("- one\n- two");
    expect(md("<ol><li>one</li><li>two</li><li>three</li></ol>")).toBe("1. one\n2. two\n3. three");
    expect(md("text<ul><li>one</li></ul>after")).toBe("text\n- one\nafter");
    expect(md("<ul><li><b>bold</b> item</li><li></li><li>last</li></ul>")).toBe("- **bold** item\n- last");
    expect(md("<ul><li>- starts like a list</li></ul>")).toBe("- \\- starts like a list");
  });

  it("writes nested lists", () => {
    expect(md("<ul><li>a<ul><li>b</li></ul></li><li>c</li></ul>")).toBe("- a\n  - b\n- c");
    expect(md("<ol><li>a<ul><li>b</li></ul></li></ol>")).toBe("1. a\n   - b");
  });

  it("writes quotes", () => {
    expect(md("<blockquote>said</blockquote>")).toBe("> said");
    expect(md("<blockquote>one<div>two</div><div><br></div><div>three</div></blockquote>")).toBe("> one\n> two\n>\n> three");
    expect(md("before<blockquote>q</blockquote>after")).toBe("before\n> q\nafter");
    expect(md("<blockquote><ul><li>x</li></ul></blockquote>")).toBe("> - x");
  });

  it("drops what a message cannot hold", () => {
    expect(md('a<img src="https://evil.example/x.png">b')).toBe("ab");
    expect(md("a<script>alert(1)</script>b")).toBe("ab");
    expect(md("a<style>p{}</style>b")).toBe("ab");
  });

  it("reads through elements it has no use for", () => {
    expect(md('<span style="color:red">red</span> <font>f</font> <u>u</u>')).toBe("red f u");
    expect(md("a\u00a0b")).toBe("a b");
  });

  it("keeps a block inside an inline element on its own line", () => {
    expect(md("<b>one<div>two</div></b>")).toBe("**one\ntwo**");
  });
});

describe("characters", () => {
  it("counts what a person counts", () => {
    expect(characters("")).toBe(0);
    expect(characters("héllo")).toBe(5);
    expect(characters("👋🏽")).toBe(2);
    expect(characters("👋")).toBe(1);
  });
});

describe("mountEditor", () => {
  function mount() {
    document.body.innerHTML = `
      <div id="toolbar"><button data-cmd="bold">B</button></div>
      <div id="editor" contenteditable="true"></div>`;
    const editor = document.getElementById("editor");
    const toolbar = document.getElementById("toolbar");
    const seen = [];
    const handle = mountEditor(editor, toolbar, (markdown) => seen.push(markdown));
    return { editor, handle, seen };
  }

  it("reports the Markdown after every edit", () => {
    const { editor, seen } = mount();
    editor.innerHTML = "hi <b>there</b>";
    editor.dispatchEvent(new Event("input"));
    expect(seen.at(-1)).toBe("hi **there**");
  });

  it("empties a box whose content was all deleted, so the placeholder shows", () => {
    const { editor, seen } = mount();
    editor.innerHTML = "<br>";
    editor.dispatchEvent(new Event("input"));
    expect(editor.innerHTML).toBe("");
    expect(seen.at(-1)).toBe("");
  });

  it("clears", () => {
    const { editor, handle, seen } = mount();
    editor.innerHTML = "something";
    handle.clear();
    expect(editor.innerHTML).toBe("");
    expect(seen.at(-1)).toBe("");
    expect(handle.markdown()).toBe("");
  });

  it("refuses dropped content", () => {
    const { editor } = mount();
    const drop = new Event("drop", { cancelable: true });
    editor.dispatchEvent(drop);
    expect(drop.defaultPrevented).toBe(true);
  });
});
