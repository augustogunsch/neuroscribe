"use strict";

/* The Markdown editor's toolbar: what each button does to the textarea.
 *
 * The buttons themselves are built in router.js, next to the rest of the
 * chapter page; these are the edits they perform, kept apart because they are
 * pure text manipulation and the only thing in the editor that can be reasoned
 * about without a DOM around it.
 */

/* ---- markdown editor: toolbar + shortcuts + image upload ---- */

function mdTextarea(el) {
	var form = el.closest("form");
	return form ? form.querySelector('textarea[name="content"]') : null;
}

function mdWrap(ta, before, after, placeholder) {
	var start = ta.selectionStart, end = ta.selectionEnd;
	var sel = ta.value.slice(start, end) || placeholder;
	ta.setRangeText(before + sel + after, start, end, "end");
	ta.selectionStart = start + before.length;
	ta.selectionEnd = start + before.length + sel.length;
	ta.focus();
	ta.dispatchEvent(new Event("input", { bubbles: true }));
}

function mdLinePrefix(ta, prefix) {
	var start = ta.selectionStart;
	var lineStart = ta.value.lastIndexOf("\n", start - 1) + 1;
	ta.setRangeText(prefix, lineStart, lineStart, "end");
	ta.selectionStart = ta.selectionEnd = start + prefix.length;
	ta.focus();
}

function mdInsertBlock(ta, text) {
	var start = ta.selectionStart;
	var needsNL = start > 0 && ta.value[start - 1] !== "\n" ? "\n" : "";
	ta.setRangeText(needsNL + text, start, ta.selectionEnd, "end");
	ta.focus();
}

function mdAction(action, toolbar) {
	var ta = mdTextarea(toolbar);
	if (!ta) return;
	switch (action) {
		case "bold": mdWrap(ta, "**", "**", "bold"); break;
		case "italic": mdWrap(ta, "*", "*", "italic"); break;
		case "strike": mdWrap(ta, "~~", "~~", "text"); break;
		case "code": mdWrap(ta, "`", "`", "code"); break;
		case "math": mdWrap(ta, "$", "$", "x^2"); break;
		case "link": mdWrap(ta, "[", "](https://)", "text"); break;
		// ### rather than ##: the chapter title is the page's h1 and rendered
		// Markdown starts one level below it, so an inserted heading should
		// sit under the title, not compete with it
		case "heading": mdLinePrefix(ta, "### "); break;
		case "list": mdLinePrefix(ta, "- "); break;
		case "quote": mdLinePrefix(ta, "> "); break;
		case "codeblock": mdInsertBlock(ta, "```python\n\n```\n"); break;
		case "image": toolbar.querySelector(".img-input").click(); break;
	}
}
