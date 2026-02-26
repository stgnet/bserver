#!/usr/bin/env python3
"""
php2yaml.py — Convert PHP/HTML files into bserver YAML pages.

Parses existing .php and .html files and produces bserver-style YAML that
replicates the same HTML output.  Static HTML is converted to YAML content
trees; PHP code blocks become inline ``script: php`` entries so they still
execute server-side.

When multiple input files are provided, cross-page analysis finds repeated
header/footer content and shared formatting patterns, splitting them into
separate YAML files (header.yaml, footer.yaml, formats, etc.).

Usage:
    python3 php2yaml.py page.php                    # single file
    python3 php2yaml.py *.php                       # batch conversion
    python3 php2yaml.py --outdir yaml_out *.php     # write to a directory
    python3 php2yaml.py --check page.php            # dry-run, print to stdout
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import os
import re
import sys
import textwrap
from collections import Counter, OrderedDict
from html.parser import HTMLParser
from typing import Any

# ─── YAML output (no dependency on PyYAML) ─────────────────────────────

def yaml_dump(data: Any, indent: int = 0, flow: bool = False) -> str:
    """Minimal YAML serializer that produces clean bserver-style output."""
    pad = "  " * indent

    if data is None:
        return ""

    if isinstance(data, str):
        # Multi-line strings use literal block scalar
        if "\n" in data:
            header = f"|"
            lines = data.rstrip("\n").split("\n")
            # Replace tabs with spaces — tabs are illegal in YAML indentation
            body = "\n".join(f"{pad}  {l.expandtabs(4)}" for l in lines)
            return f"{header}\n{body}"
        if _needs_quoting(data):
            return _quote(data)
        return data

    if isinstance(data, bool):
        return "true" if data else "false"

    if isinstance(data, (int, float)):
        return str(data)

    if isinstance(data, list):
        if not data:
            return "[]"
        # Short list of simple scalars? Use flow style.
        if flow or (all(isinstance(v, str) and "\n" not in v and len(v) < 60 for v in data) and len(data) <= 4):
            items = ", ".join(_quote(v) if _needs_quoting(v) else v for v in data)
            return f"[{items}]"
        lines = []
        for item in data:
            if isinstance(item, dict) and len(item) == 1:
                k, v = next(iter(item.items()))
                if v is None:
                    lines.append(f"{pad}- {_yaml_key(k)}:")
                elif isinstance(v, str) and "\n" not in v:
                    val = yaml_dump(v, indent + 1)
                    lines.append(f"{pad}- {_yaml_key(k)}: {val}")
                elif isinstance(v, str) and "\n" in v:
                    # Multi-line string: put | on same line as key
                    body_pad = "  " * (indent + 2)
                    body_lines = v.rstrip("\n").split("\n")
                    body = "\n".join(f"{body_pad}{l.expandtabs(4)}" for l in body_lines)
                    lines.append(f"{pad}- {_yaml_key(k)}: |\n{body}")
                else:
                    # Generate value without padding, then indent to correct level
                    val = yaml_dump(v, 0)
                    lines.append(f"{pad}- {_yaml_key(k)}:\n{_indent_block(val, indent + 2)}")
            elif isinstance(item, dict):
                first = True
                for k, v in item.items():
                    prefix = "- " if first else "  "
                    first = False
                    if isinstance(v, (str, int, float, bool)) and "\n" not in str(v):
                        val = yaml_dump(v, 0)
                        lines.append(f"{pad}{prefix}{_yaml_key(k)}: {val}")
                    elif isinstance(v, str) and "\n" in v:
                        # Multi-line string: put | on same line as key
                        body_pad = "  " * (indent + 2)
                        body_lines = v.rstrip("\n").split("\n")
                        body = "\n".join(f"{body_pad}{l.expandtabs(4)}" for l in body_lines)
                        lines.append(f"{pad}{prefix}{_yaml_key(k)}: |\n{body}")
                    else:
                        # Generate value without padding, then indent to correct level
                        val = yaml_dump(v, 0)
                        lines.append(f"{pad}{prefix}{_yaml_key(k)}:")
                        lines.append(_indent_block(val, indent + 2))
            elif isinstance(item, str):
                lines.append(f"{pad}- {_quote(item) if _needs_quoting(item) else item}")
            elif isinstance(item, list):
                # Generate value without padding, then indent to correct level
                val = yaml_dump(item, 0)
                lines.append(f"{pad}-\n{_indent_block(val, indent + 1)}")
            else:
                lines.append(f"{pad}- {yaml_dump(item, indent + 1)}")
        return "\n".join(lines)

    if isinstance(data, dict):
        if not data:
            return "{}"
        lines = []
        for k, v in data.items():
            val = yaml_dump(v, indent + 1)
            if v is None or (isinstance(v, str) and v == ""):
                lines.append(f"{pad}{_yaml_key(k)}:")
            elif isinstance(v, (str, int, float, bool)) and "\n" not in str(v):
                lines.append(f"{pad}{_yaml_key(k)}: {val}")
            elif isinstance(v, list):
                lines.append(f"{pad}{_yaml_key(k)}:")
                lines.append(val)
            elif isinstance(v, dict):
                lines.append(f"{pad}{_yaml_key(k)}:")
                lines.append(val)
            else:
                lines.append(f"{pad}{_yaml_key(k)}: {val}")
        return "\n".join(lines)

    return str(data)


def _needs_quoting(s: str) -> bool:
    if not s:
        return True
    if s in ("true", "false", "null", "yes", "no", "on", "off"):
        return True
    if s[0] in ("'", '"', "{", "[", ">", "|", "*", "&", "!", "%", "@", "#"):
        return True
    if ": " in s or s.endswith(":"):
        return True
    if "\t" in s:
        return True  # tabs cannot appear in unquoted YAML scalars
    if s.startswith("- "):
        return True
    try:
        float(s)
        return True
    except ValueError:
        pass
    return False


def _quote(s: str) -> str:
    if "'" not in s:
        return f"'{s}'"
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _yaml_key(k: str) -> str:
    if _needs_quoting(k):
        return _quote(k)
    return k


def _indent_block(text: str, indent: int) -> str:
    pad = "  " * indent
    return "\n".join(f"{pad}{l}" if l.strip() else "" for l in text.split("\n"))


# ─── HTML / PHP Parser ─────────────────────────────────────────────────

# HTML tags that bserver renders directly (no ^format needed)
NATIVE_TAGS = {
    "h1", "h2", "h3", "h4", "h5", "h6",
    "p", "a", "span", "div", "section", "article", "aside", "nav",
    "ul", "ol", "li", "dl", "dt", "dd",
    "table", "thead", "tbody", "tfoot", "tr", "th", "td",
    "form", "input", "textarea", "select", "option", "button", "label",
    "img", "br", "hr",
    "pre", "code", "blockquote", "em", "strong", "b", "i", "u", "small",
    "header", "footer", "main",
    "script", "style", "link", "meta", "title",
}

VOID_TAGS = {"br", "hr", "img", "input", "link", "meta", "area", "base", "col", "embed", "source", "track", "wbr"}

# Tags whose text content should be preserved verbatim
PREFORMATTED_TAGS = {"pre", "code", "script", "style", "textarea"}


class DOMNode:
    """Lightweight DOM node for the parsed HTML tree."""

    def __init__(self, tag: str = "", attrs: list[tuple[str, str | None]] | None = None):
        self.tag = tag                          # "" for text/php nodes
        self.attrs: dict[str, str] = {}
        if attrs:
            for k, v in attrs:
                self.attrs[k] = v if v is not None else ""
        self.children: list[DOMNode] = []
        self.text: str = ""                     # for text nodes
        self.php_code: str = ""                 # for PHP code blocks
        self.is_text = False
        self.is_php = False

    def __repr__(self):
        if self.is_text:
            return f"Text({self.text[:40]!r})"
        if self.is_php:
            return f"PHP({self.php_code[:40]!r})"
        return f"<{self.tag}>"

    def text_content(self) -> str:
        """Get concatenated text of all descendants (no PHP)."""
        if self.is_text:
            return self.text
        return "".join(c.text_content() for c in self.children)

    def has_php(self) -> bool:
        """Check if this subtree contains any PHP code."""
        if self.is_php:
            return True
        return any(c.has_php() for c in self.children)

    def has_only_text(self) -> bool:
        """True if children are only text nodes (no tags, no PHP)."""
        return all(c.is_text for c in self.children)

    def inner_html(self) -> str:
        """Reconstruct inner HTML (for PHP-containing subtrees)."""
        parts = []
        for c in self.children:
            if c.is_text:
                parts.append(c.text)
            elif c.is_php:
                parts.append(f"<?php {c.php_code} ?>")
            else:
                parts.append(c.outer_html())
        return "".join(parts)

    def outer_html(self) -> str:
        """Reconstruct outer HTML."""
        if self.is_text:
            return self.text
        if self.is_php:
            return f"<?php {self.php_code} ?>"
        attrs_str = ""
        for k, v in self.attrs.items():
            if v:
                attrs_str += f' {k}="{v}"'
            else:
                attrs_str += f" {k}"
        if self.tag in VOID_TAGS:
            return f"<{self.tag}{attrs_str}>"
        inner = self.inner_html()
        return f"<{self.tag}{attrs_str}>{inner}</{self.tag}>"

    def structural_signature(self) -> str:
        """Hash of the tag/class structure (ignoring text content)."""
        parts = [self.tag]
        if "class" in self.attrs:
            parts.append(self.attrs["class"])
        for c in self.children:
            if not c.is_text and not c.is_php:
                parts.append(c.structural_signature())
        return hashlib.md5("|".join(parts).encode()).hexdigest()[:8]


class PHPHTMLParser(HTMLParser):
    """
    Parses a PHP/HTML file into a DOM tree.

    PHP blocks (<?php ... ?> and <?= ... ?>) are extracted first and replaced
    with sentinel markers, then the HTML is parsed normally, and finally the
    sentinels are mapped back to PHP code nodes.
    """

    PHP_SENTINEL = "\x00PHP_BLOCK_%d\x00"
    PHP_RE = re.compile(
        r"<\?(?:php)?\s(.*?)(?:\?>|\Z)|<\?=(.*?)\?>",
        re.DOTALL,
    )

    def __init__(self):
        super().__init__()
        self.root = DOMNode(tag="__root__")
        self._stack: list[DOMNode] = [self.root]
        self._php_blocks: dict[str, str] = {}
        self._php_is_echo: dict[str, bool] = {}

    @property
    def _current(self) -> DOMNode:
        return self._stack[-1]

    def parse_file(self, source: str) -> DOMNode:
        """Parse PHP/HTML source and return the root DOMNode."""
        # Extract PHP blocks and replace with sentinels
        counter = [0]
        def _replace_php(m):
            idx = counter[0]
            counter[0] += 1
            sentinel = self.PHP_SENTINEL % idx
            code = m.group(1) if m.group(1) is not None else m.group(2)
            is_echo = m.group(2) is not None
            self._php_blocks[sentinel] = textwrap.dedent(code).strip()
            self._php_is_echo[sentinel] = is_echo
            return sentinel
        cleaned = self.PHP_RE.sub(_replace_php, source)

        self.feed(cleaned)
        self._resolve_php(self.root)
        return self.root

    def _resolve_php(self, node: DOMNode):
        """Walk the tree, split text nodes with PHP sentinels, and resolve attrs."""
        # Resolve PHP sentinels in attribute values
        sentinel_re = re.compile(r"\x00PHP_BLOCK_(\d+)\x00")
        for attr_key in list(node.attrs.keys()):
            val = node.attrs[attr_key]
            if val and "\x00" in val:
                # Replace sentinels with PHP expression concatenation
                def _attr_repl(m):
                    sentinel = m.group(0)
                    code = self._php_blocks.get(sentinel, "")
                    is_echo = self._php_is_echo.get(sentinel, False)
                    # Mark this node as containing PHP (since its attrs do)
                    node._attr_has_php = True
                    if is_echo:
                        return f"{{{{ {code} }}}}"  # template marker
                    return f"{{{{ {code} }}}}"
                node.attrs[attr_key] = sentinel_re.sub(_attr_repl, val)

        new_children = []
        for child in node.children:
            if child.is_text:
                parts = self._split_php_sentinels(child.text)
                new_children.extend(parts)
            else:
                self._resolve_php(child)
                new_children.append(child)
        node.children = new_children

    def _split_php_sentinels(self, text: str) -> list[DOMNode]:
        """Split a text string on PHP sentinels, returning text and PHP nodes."""
        nodes = []
        pattern = re.compile(r"(\x00PHP_BLOCK_\d+\x00)")
        parts = pattern.split(text)
        for part in parts:
            if not part:
                continue
            if part in self._php_blocks:
                n = DOMNode()
                n.is_php = True
                code = self._php_blocks[part]
                if self._php_is_echo[part]:
                    # <?= expr ?> → echo expr;
                    code = f"echo {code};"
                n.php_code = code
                nodes.append(n)
            else:
                if part.strip():
                    n = DOMNode()
                    n.is_text = True
                    n.text = part
                    nodes.append(n)
        return nodes

    # ── HTMLParser callbacks ────────────────────────────────────────────

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]):
        node = DOMNode(tag=tag, attrs=attrs)
        self._current.children.append(node)
        if tag not in VOID_TAGS:
            self._stack.append(node)

    def handle_endtag(self, tag: str):
        # Pop up to the matching tag (tolerant of mis-nested HTML)
        for i in range(len(self._stack) - 1, 0, -1):
            if self._stack[i].tag == tag:
                self._stack = self._stack[: i]
                return

    def handle_data(self, data: str):
        if data.strip() or "\x00" in data:
            node = DOMNode()
            node.is_text = True
            node.text = data
            self._current.children.append(node)

    def handle_comment(self, data: str):
        pass  # drop HTML comments

    def handle_decl(self, decl: str):
        pass  # drop <!DOCTYPE>


# ─── DOM → YAML conversion ─────────────────────────────────────────────

# bserver already has these built-in, so we emit their short names
BUILTIN_FORMATS = {
    "ulist", "links", "link", "image", "muted", "card", "container",
    "row", "col", "section", "code", "navbar",
}

# Block-level tags that can be converted to markdown
MARKDOWN_BLOCK_TAGS = {"h1", "h2", "h3", "h4", "h5", "h6", "p", "ul", "ol", "hr", "br", "pre", "blockquote", "img"}

# Inline tags that have markdown equivalents
MARKDOWN_INLINE_TAGS = {"a", "strong", "b", "em", "i", "code", "br", "span"}

# Minimum consecutive markdown-compatible nodes to trigger grouping
MIN_MARKDOWN_RUN = 2

# Builtin formats whose content: definition already wraps children in a
# sub-format.  When php2yaml sees e.g. <div class="card"><div class="card-body">
# content</div></div>, it should output  card: content  instead of
# card: {card-body: content}  because ^card already wraps with cardbody.
BUILTIN_CONTENT_WRAPS = {
    "card": {"card-body", "cardbody"},
}


class PageConverter:
    """Converts a parsed DOM tree into a bserver YAML page structure."""

    def __init__(self, filename: str):
        self.filename = filename
        self.title: str = ""
        self.meta: dict[str, str] = OrderedDict()
        self.styles: list[str] = []                  # raw CSS
        self.style_links: list[dict[str, str]] = []  # <link rel=stylesheet>
        self.scripts_head: list[str] = []            # <script> in head
        self.php_init: list[str] = []                 # PHP init code (before <html>)
        self.header_content: list[Any] = []           # content from <header>
        self.main_content: list[Any] = []             # YAML content items
        self.footer_content: list[Any] = []           # content from <footer>
        self.formats: dict[str, dict] = OrderedDict() # ^name definitions
        self._php_counter = 0
        self._format_name_counts: dict[str, int] = {} # de-duplicate names

    def convert(self, root: DOMNode) -> dict[str, Any]:
        """Convert a DOM root into a dict ready for YAML output."""
        # Collect PHP code that appears before <html> (init/setup code)
        html_node = self._find_tag(root, "html")
        if html_node:
            for child in root.children:
                if child is html_node:
                    break
                if child.is_php:
                    self.php_init.append(child.php_code)

        # Find <html>, <head>, <body> if present
        if not html_node:
            html_node = self._find_tag(root, "html")
        if html_node:
            head_node = self._find_tag(html_node, "head")
            body_node = self._find_tag(html_node, "body")
        else:
            head_node = self._find_tag(root, "head")
            body_node = self._find_tag(root, "body")

        if head_node:
            self._process_head(head_node)

        content_root = body_node or html_node or root

        # Split body into header / main / footer sections (HTML5 semantic tags)
        header_node = self._find_direct_child(content_root, "header")
        main_node = self._find_direct_child(content_root, "main")
        footer_node = self._find_direct_child(content_root, "footer")

        if main_node:
            # Structured page: separate header, main, footer
            # Collect everything before <main> as header content
            header_items = []
            if header_node:
                header_items = self._convert_children(header_node)
            else:
                # No <header> tag — collect elements before <main> as header
                pre_main = []
                for child in content_root.children:
                    if child is main_node:
                        break
                    pre_main.append(child)
                header_items = self._convert_children(content_root, pre_main)
            self.header_content = header_items

            self.main_content = self._convert_children(main_node)

            if footer_node:
                self.footer_content = self._convert_children(footer_node)
        else:
            # No <main> tag — treat entire body as main content,
            # but still extract <header> and <footer> if present
            if header_node:
                self.header_content = self._convert_children(header_node)
            if footer_node:
                self.footer_content = self._convert_children(footer_node)
            # Everything else in body goes to main
            remaining_children = [child for child in content_root.children
                                  if child is not header_node and child is not footer_node]
            self.main_content = self._convert_children(content_root, remaining_children)

        return self._build_output()

    def _find_direct_child(self, node: DOMNode, tag: str) -> DOMNode | None:
        """Find a direct child element with the given tag."""
        for child in node.children:
            if child.tag == tag:
                return child
        return None

    def _find_tag(self, node: DOMNode, tag: str) -> DOMNode | None:
        for child in node.children:
            if child.tag == tag:
                return child
            found = self._find_tag(child, tag)
            if found:
                return found
        return None

    def _process_head(self, head: DOMNode):
        for child in head.children:
            if child.tag == "title":
                if child.has_php():
                    # Dynamic title — use the PHP code
                    self.title = child.text_content().strip() or "Dynamic Title"
                else:
                    self.title = child.text_content().strip()
            elif child.tag == "meta":
                name = child.attrs.get("name", "")
                content = child.attrs.get("content", "")
                if name and content:
                    self.meta[name] = content
            elif child.tag == "link" and "stylesheet" in child.attrs.get("rel", ""):
                self.style_links.append(dict(child.attrs))
            elif child.tag == "style":
                css = child.text_content().strip()
                if css:
                    self.styles.append(css)
            elif child.tag == "script":
                src = child.attrs.get("src", "")
                if src:
                    self.scripts_head.append(src)

    # ── Markdown grouping ────────────────────────────────────────────

    def _is_markdown_block(self, node: DOMNode) -> bool:
        """Check if a DOM node can be represented as a markdown block."""
        if node.is_php:
            return False
        if node.is_text:
            # Whitespace-only text between block elements is fine
            return node.text.strip() == ""
        if node.has_php():
            return False
        tag = node.tag.lower()
        if tag not in MARKDOWN_BLOCK_TAGS:
            return False
        if self._has_significant_attrs(node):
            return False
        # Children must be markdown-inline compatible
        return self._has_markdown_inline_only(node)

    def _has_markdown_inline_only(self, node: DOMNode) -> bool:
        """Check if all children are text or markdown-compatible tags."""
        for child in node.children:
            if child.is_text:
                continue
            if child.is_php:
                return False
            tag = child.tag.lower()
            if tag == "li":
                # li is valid inside ul/ol
                if not self._has_markdown_inline_only(child):
                    return False
                continue
            # Allow block-level markdown children (e.g. <ul> inside <p>)
            if tag in MARKDOWN_BLOCK_TAGS:
                if self._has_significant_attrs(child) and tag != "img":
                    return False
                if not self._has_markdown_inline_only(child):
                    return False
                continue
            if tag not in MARKDOWN_INLINE_TAGS:
                return False
            if self._has_significant_attrs(child):
                if tag == "a":
                    # <a> with href is fine (href is not "significant")
                    non_href = {k for k in child.attrs if k != "href"}
                    if any(k in {"class", "id", "style", "role"} or k.startswith("data-")
                           for k in non_href):
                        return False
                else:
                    return False
            if not self._has_markdown_inline_only(child):
                return False
        return True

    def _inline_to_markdown(self, node: DOMNode) -> str:
        """Convert a node's inline content to markdown text."""
        parts = []
        for child in node.children:
            if child.is_text:
                # Keep original line breaks; strip indentation on continuation
                # lines but preserve the first line as-is (its leading/trailing
                # space may be meaningful between inline elements).
                lines = child.text.split("\n")
                result = [lines[0]]
                for line in lines[1:]:
                    result.append(line.lstrip())
                parts.append("\n".join(result))
                continue
            tag = child.tag.lower()
            if tag == "a":
                href = child.attrs.get("href", "").strip()
                text = self._inline_to_markdown(child).strip()
                parts.append(f"[{text}]({href})")
            elif tag in ("strong", "b"):
                text = self._inline_to_markdown(child).strip()
                parts.append(f"**{text}**")
            elif tag in ("em", "i"):
                text = self._inline_to_markdown(child)
                parts.append(f"*{text}*")
            elif tag == "code":
                text = child.text_content()
                parts.append(f"`{text}`")
            elif tag == "br":
                parts.append("  \n")
            elif tag == "img":
                src = child.attrs.get("src", "")
                alt = child.attrs.get("alt", "")
                parts.append(f"![{alt}]({src})")
            elif tag == "span":
                parts.append(self._inline_to_markdown(child))
            elif tag in MARKDOWN_BLOCK_TAGS:
                # Block element nested inside inline context (e.g. <ul> inside <p>)
                md = self._node_to_markdown(child)
                if md:
                    parts.append("\n\n" + md + "\n\n")
            else:
                parts.append(child.text_content())
        return "".join(parts)

    def _node_to_markdown(self, node: DOMNode) -> str:
        """Convert a single DOM node to a markdown string."""
        if node.is_text:
            return ""  # whitespace between blocks, skip
        tag = node.tag.lower()
        if tag in ("h1", "h2", "h3", "h4", "h5", "h6"):
            level = int(tag[1])
            text = self._inline_to_markdown(node).strip()
            return "#" * level + " " + text
        if tag == "p":
            text = self._inline_to_markdown(node).strip()
            # Collapse triple+ newlines to double (avoid excessive blank lines)
            text = re.sub(r'\n{3,}', '\n\n', text)
            return text
        if tag == "ul":
            lines = []
            for li in node.children:
                if li.is_text and not li.text.strip():
                    continue
                if not li.is_text and li.tag.lower() == "li":
                    text = self._inline_to_markdown(li).strip()
                    lines.append("- " + text)
            return "\n".join(lines)
        if tag == "ol":
            lines = []
            num = 1
            for li in node.children:
                if li.is_text and not li.text.strip():
                    continue
                if not li.is_text and li.tag.lower() == "li":
                    text = self._inline_to_markdown(li).strip()
                    lines.append(f"{num}. " + text)
                    num += 1
            return "\n".join(lines)
        if tag == "img":
            src = node.attrs.get("src", "")
            alt = node.attrs.get("alt", "")
            return f"![{alt}]({src})"
        if tag == "hr":
            return "---"
        if tag == "br":
            return ""
        if tag == "pre":
            text = node.text_content()
            return "```\n" + text + "\n```"
        if tag == "blockquote":
            text = self._inline_to_markdown(node).strip()
            lines = text.split("\n")
            return "\n".join("> " + line for line in lines)
        return ""

    def _group_markdown(self, children: list[DOMNode]) -> list[Any]:
        """Convert children, grouping consecutive markdown-compatible nodes.

        Scans children at the DOM level. Consecutive runs of markdown-compatible
        block elements are collapsed into a single {markdown: "..."} entry.
        Other children are converted through the normal _convert_node path.
        """
        items = []
        i = 0
        while i < len(children):
            child = children[i]
            if self._is_markdown_block(child):
                # Count the run of consecutive markdown-compatible nodes
                j = i
                while j < len(children) and self._is_markdown_block(children[j]):
                    j += 1
                run_length = j - i
                # Only count non-whitespace nodes
                real_nodes = [children[k] for k in range(i, j) if not children[k].is_text]
                if len(real_nodes) >= MIN_MARKDOWN_RUN:
                    md_parts = []
                    for k in range(i, j):
                        md_text = self._node_to_markdown(children[k])
                        if md_text:
                            md_parts.append(md_text)
                    if md_parts:
                        md_text = "\n\n".join(md_parts) + "\n"
                        # Collapse triple+ newlines to double
                        md_text = re.sub(r'\n{3,}', '\n\n', md_text)
                        items.append({"markdown": md_text})
                    i = j
                    continue
            # Normal conversion
            converted = self._convert_node(child)
            if converted is not None:
                if isinstance(converted, list):
                    items.extend(converted)
                else:
                    items.append(converted)
            i += 1
        return items

    # ── Child conversion ──────────────────────────────────────────────

    def _convert_children(self, node: DOMNode, children: list[DOMNode] | None = None) -> list[Any]:
        """Convert a node's children into a YAML content list."""
        raw_items = self._group_markdown(children if children is not None else node.children)
        # Post-process: merge consecutive {links: ...} dicts into one
        items = []
        for item in raw_items:
            if (isinstance(item, dict) and len(item) == 1 and "links" in item
                    and isinstance(item["links"], dict)):
                # Check if previous item is also a links dict — merge
                if (items and isinstance(items[-1], dict)
                        and len(items[-1]) == 1 and "links" in items[-1]
                        and isinstance(items[-1]["links"], dict)):
                    items[-1]["links"].update(item["links"])
                    continue
            items.append(item)
        return items

    def _convert_node(self, node: DOMNode) -> Any:
        """Convert a single DOMNode into YAML-compatible structure."""
        if node.is_text:
            # Strip per-line leading whitespace (HTML source indentation)
            lines = node.text.split("\n")
            text = "\n".join(line.strip() for line in lines)
            text = text.strip()
            return text if text else None

        if node.is_php:
            return self._make_php_block(node.php_code)

        tag = node.tag.lower()

        # Skip <script> and <link> in body (already captured from head)
        if tag == "script" and node.attrs.get("src"):
            return None
        if tag == "link":
            return None

        # If the subtree contains PHP, check whether the PHP is at direct-child
        # level (can mix YAML + PHP blocks) or deeply interleaved (needs full
        # PHP echo wrapping).
        if node.has_php():
            if self._php_only_at_child_level(node):
                # PHP blocks are direct children — convert siblings normally
                # and only the PHP nodes become inline script entries.
                # Fall through to normal container handling below.
                pass
            else:
                return self._convert_php_subtree(node)

        # Simple tags: h1-h6, p, span, etc. with only text content
        if tag in NATIVE_TAGS and node.has_only_text():
            text = node.text_content().strip()
            if not text and tag in VOID_TAGS:
                return self._convert_void_tag(node)
            if tag == "a":
                return self._convert_link(node)
            if tag == "img":
                return self._convert_img(node)
            if tag in ("ul", "ol"):
                return self._convert_list(node)
            if tag == "br":
                return {"br": None}
            if tag == "hr":
                return {"hr": None}
            if not text:
                return None
            # Tag with class or id gets a ^format
            if self._has_significant_attrs(node):
                return self._convert_formatted_tag(node)
            return {tag: text}

        # Void tags
        if tag in VOID_TAGS:
            return self._convert_void_tag(node)

        # <a> with complex content
        if tag == "a":
            return self._convert_link(node)

        # <img>
        if tag == "img":
            return self._convert_img(node)

        # <ul>/<ol>
        if tag in ("ul", "ol"):
            return self._convert_list(node)

        # <table>
        if tag == "table":
            return self._convert_table(node)

        # <form>
        if tag == "form":
            return self._convert_form(node)

        # Generic container with children
        children = self._convert_children(node)
        if not children:
            return None

        if self._has_significant_attrs(node):
            return self._convert_formatted_container(node, children)

        # If it's a known tag, just use it directly
        if tag in NATIVE_TAGS:
            if len(children) == 1 and isinstance(children[0], str):
                return {tag: children[0]}
            return {tag: children}

        # Unknown tag → format definition
        return self._convert_formatted_container(node, children)

    def _php_only_at_child_level(self, node: DOMNode) -> bool:
        """True if all PHP in this subtree is at the direct-child level.

        When PHP blocks are direct children of a container, we can convert
        the non-PHP siblings normally and only wrap PHP nodes as inline
        scripts.  When PHP is deeper (e.g. inside an attribute value or
        interleaved mid-tag), we need the full echo-wrapping approach.
        """
        for child in node.children:
            if child.is_php:
                continue  # direct child PHP — fine
            if child.is_text:
                continue
            if child.has_php():
                return False  # PHP is nested deeper
        return True

    def _has_significant_attrs(self, node: DOMNode) -> bool:
        """Check if node has class, id, or other meaningful attributes."""
        dominated_by = {"class", "id", "style", "role", "data-"}
        for k in node.attrs:
            if k in dominated_by or k.startswith("data-"):
                return True
        return False

    def _convert_link(self, node: DOMNode) -> dict:
        href = node.attrs.get("href", "")
        text = node.text_content().strip()
        if text and not node.has_php():
            return {"links": {href: text}}
        # Complex link content
        children = self._convert_children(node)
        return {"a": children} if children else {"links": {href: text or href}}

    def _convert_img(self, node: DOMNode) -> dict:
        src = node.attrs.get("src", "")
        alt = node.attrs.get("alt", "")
        if alt:
            return {"image": src}  # bserver's built-in image format
        return {"image": src}

    def _convert_list(self, node: DOMNode) -> dict:
        items = []
        for child in node.children:
            if child.tag == "li":
                if child.has_only_text():
                    items.append(child.text_content().strip())
                elif child.has_php():
                    items.append(self._convert_php_subtree(child))
                else:
                    sub = self._convert_children(child)
                    if len(sub) == 1:
                        items.append(sub[0])
                    elif sub:
                        items.append(sub)
        tag = "ulist" if node.tag == "ul" else "ol"
        if tag == "ulist":
            return {"ulist": items}
        # Ordered list needs a format
        fmt_name = "olist"
        if fmt_name not in self.formats:
            self.formats[fmt_name] = {
                "tag": "ol",
                "contents": {"li": "$*"},
            }
        return {fmt_name: items}

    def _convert_table(self, node: DOMNode) -> dict:
        """Convert <table> to YAML — tables are rendered as raw HTML
        since bserver doesn't have a native table abstraction yet."""
        return self._emit_raw_html(node)

    def _convert_form(self, node: DOMNode) -> dict:
        """Convert <form> to raw HTML, preserving PHP processing."""
        if node.has_php():
            return self._convert_php_subtree(node)
        return self._emit_raw_html(node)

    def _convert_void_tag(self, node: DOMNode) -> dict | None:
        tag = node.tag
        if tag == "br":
            return {"br": None}
        if tag == "hr":
            return {"hr": None}
        if tag == "img":
            return self._convert_img(node)
        if tag == "input":
            return self._emit_raw_html(node)
        if tag == "meta":
            return None  # handled in head
        return None

    def _emit_raw_html(self, node: DOMNode) -> dict:
        """Emit a node as literal HTML via a format with raw content."""
        html_str = node.outer_html()
        fmt_name = f"raw_{node.tag}_{self._php_counter}"
        self._php_counter += 1
        self.formats[fmt_name] = {
            "tag": "div",
            "content": "$*",
        }
        return {fmt_name: html_str}

    def _convert_formatted_tag(self, node: DOMNode) -> dict:
        """Convert a tag with significant CSS classes into a ^format + content."""
        tag = node.tag
        cls = node.attrs.get("class", "")
        fmt_name = self._make_format_name(tag, cls)

        if fmt_name not in self.formats:
            fmt = {"tag": tag}
            params = OrderedDict()
            for k, v in node.attrs.items():
                if k not in ("href", "src"):
                    params[k] = v
            if params:
                fmt["params"] = dict(params)
            fmt["content"] = "$*"
            self.formats[fmt_name] = fmt

        text = node.text_content().strip()
        return {fmt_name: text}

    def _convert_formatted_container(self, node: DOMNode, children: list) -> dict:
        """Convert a container tag (div, section, etc.) with classes to ^format."""
        tag = node.tag
        cls = node.attrs.get("class", "")
        fmt_name = self._make_format_name(tag, cls)

        if fmt_name not in self.formats:
            fmt = {"tag": tag}
            params = OrderedDict()
            for k, v in node.attrs.items():
                params[k] = v
            if params:
                fmt["params"] = dict(params)
            self.formats[fmt_name] = fmt

        # If this is a builtin format that already wraps with a sub-format
        # (e.g. card wraps with card-body), unwrap the child if it matches.
        if fmt_name in BUILTIN_CONTENT_WRAPS:
            wrap_names = BUILTIN_CONTENT_WRAPS[fmt_name]
            children = self._unwrap_builtin_children(children, wrap_names)

        if len(children) == 1:
            return {fmt_name: children[0]}
        return {fmt_name: children}

    def _unwrap_builtin_children(self, children: list, wrap_names: set) -> list:
        """Unwrap children that match a builtin's content wrapper.

        If the sole child is a dict like {card-body: content}, and card-body
        is in wrap_names, return the inner content as the children instead.
        """
        if len(children) == 1 and isinstance(children[0], dict):
            child = children[0]
            if len(child) == 1:
                key = next(iter(child))
                # Match by class-derived name (card-body, cardbody, etc.)
                if key in wrap_names:
                    inner = child[key]
                    if isinstance(inner, list):
                        return inner
                    return [inner]
        return children

    def _make_format_name(self, tag: str, css_class: str) -> str:
        """Generate a descriptive format name from tag and CSS class."""
        if css_class:
            # Use the first meaningful class word
            parts = css_class.split()
            name = parts[0].replace(".", "-").replace("/", "-")
            # Clean up to valid YAML identifier
            name = re.sub(r"[^a-zA-Z0-9_-]", "", name)
            if name and name[0].isdigit():
                name = f"{tag}-{name}"
            if not name:
                name = tag
        else:
            name = tag
        # If this format name already exists with different params, make unique
        if name in self.formats:
            return name  # reuse existing format
        return name

    def _convert_php_subtree(self, node: DOMNode) -> dict:
        """Convert a subtree containing PHP into an inline script block."""
        php_parts = self._extract_php_flow(node)
        return self._make_php_block(php_parts)

    def _extract_php_flow(self, node: DOMNode) -> str:
        """Extract the PHP code + surrounding HTML as a PHP script."""
        # Reconstruct the inner HTML with PHP blocks, then wrap it so
        # the PHP script echoes the static HTML and executes the PHP.
        inner = node.inner_html() if not node.is_php else node.php_code
        if node.is_php:
            return node.php_code

        # Build a PHP script that reproduces this subtree's output
        return self._html_to_php_echo(node)

    def _html_to_php_echo(self, node: DOMNode) -> str:
        """Convert a DOM subtree with embedded PHP into a single PHP script."""
        lines = []
        self._emit_php_lines(node, lines, is_root=True)
        return "\n".join(lines)

    def _emit_php_lines(self, node: DOMNode, lines: list[str], is_root: bool = False):
        """Recursively emit PHP echo statements for the node."""
        if node.is_text:
            text = node.text.strip()
            if text:
                lines.append(f"echo {_php_quote(text)};")
            return
        if node.is_php:
            lines.append(node.php_code)
            return

        # Open tag
        if not is_root and node.tag:
            has_php_attr = any("{{" in v for v in node.attrs.values())

            if has_php_attr:
                # Some attributes contain PHP expressions — use concatenation
                echo_parts = [_php_quote(f"<{node.tag}")]
                for k, v in node.attrs.items():
                    if "{{" in v:
                        echo_parts.append(_attr_to_php_concat(k, v))
                    else:
                        echo_parts.append(_php_quote(f' {k}="{v}"'))
                echo_parts.append(_php_quote(">"))
                line = f"echo {' . '.join(echo_parts)};"
                lines.append(line)
                if node.tag in VOID_TAGS:
                    return
            else:
                attrs_str = ""
                for k, v in node.attrs.items():
                    attrs_str += f' {k}="{v}"'
                if node.tag in VOID_TAGS:
                    lines.append(f"echo {_php_quote(f'<{node.tag}{attrs_str}>')};")
                    return
                lines.append(f"echo {_php_quote(f'<{node.tag}{attrs_str}>')};")

        for child in node.children:
            self._emit_php_lines(child, lines)

        # Close tag
        if not is_root and node.tag and node.tag not in VOID_TAGS:
            lines.append(f"echo {_php_quote(f'</{node.tag}>')};")

    def _make_php_block(self, code: str) -> dict:
        """Create a YAML inline PHP script entry using the php: name."""
        code = textwrap.dedent(code).strip()
        if not code.endswith("\n"):
            code += "\n"
        return {"php": code}

    def _build_output(self) -> dict[str, Any]:
        """Assemble the final YAML document."""
        doc = OrderedDict()

        if self.title:
            doc["title"] = self.title

        if self.meta:
            doc["+meta"] = [{"name": k, "content": v} for k, v in self.meta.items() if k != "viewport"]

        if self.style_links:
            links = []
            for sl in self.style_links:
                entry = OrderedDict()
                entry["rel"] = "stylesheet"
                entry["href"] = sl.get("href", "")
                for k, v in sl.items():
                    if k not in ("rel", "href"):
                        entry[k] = v
                links.append(dict(entry))
            doc["+headlink"] = links

        if self.styles:
            doc["style"] = self._parse_inline_css(self.styles)

        if self.scripts_head:
            for i, src in enumerate(self.scripts_head):
                name = f"head_script_{i}"
                doc[name] = None
                self.formats[name] = {
                    "tag": "script",
                    "params": {"src": src},
                }

        # PHP init code (variable setup, includes, etc.) → inline script at top of main
        if self.php_init:
            init_code = "\n".join(self.php_init)
            self.formats["php_init"] = {
                "script": "php",
                "code": init_code + "\n" if not init_code.endswith("\n") else init_code,
            }

        # Header — in bserver, header is a separate definition referenced by body.yaml
        if self.header_content:
            doc["header"] = self.header_content

        main = self.main_content
        if self.php_init:
            main = ["php_init"] + main
        doc["main"] = main

        # Footer — in bserver, footer is a separate definition referenced by body.yaml
        if self.footer_content:
            doc["footer"] = self.footer_content

        # Append ^format definitions
        for name, fmt in self.formats.items():
            doc[f"^{name}"] = fmt

        return doc

    def _parse_inline_css(self, css_blocks: list[str]) -> dict:
        """Best-effort parse of inline CSS into bserver style: format."""
        result = OrderedDict()
        combined = "\n".join(css_blocks)
        # Simple regex-based CSS parser
        rule_re = re.compile(r"([^{]+)\{([^}]+)\}", re.MULTILINE)
        for m in rule_re.finditer(combined):
            selector = m.group(1).strip()
            body = m.group(2).strip()
            props = OrderedDict()
            for prop in body.split(";"):
                prop = prop.strip()
                if ":" in prop:
                    pk, pv = prop.split(":", 1)
                    props[pk.strip()] = pv.strip()
            if props:
                result[selector] = dict(props)
        return dict(result) if result else {"body": {}}


def _php_quote(s: str) -> str:
    """Quote a string for PHP echo, using single quotes where possible."""
    if "'" not in s:
        return f"'{s}'"
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"').replace("$", "\\$") + '"'


def _attr_to_php_concat(attr_name: str, attr_value: str) -> str:
    """Convert an attribute value containing {{ php }} markers to PHP concat.

    E.g., attr_name='href', attr_value='/edit/{{ $item["id"] }}'
    → ' . \' href=\"/edit/\' . $item["id"] . \'\"\''
    """
    parts = re.split(r"\{\{\s*(.*?)\s*\}\}", attr_value)
    # parts alternates: [literal, php_expr, literal, php_expr, ...]
    php_parts = []
    php_parts.append(f"' {attr_name}=\"'")
    for i, part in enumerate(parts):
        if i % 2 == 0:
            # Literal text
            if part:
                php_parts.append(_php_quote(part))
        else:
            # PHP expression
            php_parts.append(part)
    php_parts.append("'\"'")
    return " . ".join(php_parts)


# ─── Cross-page analysis ───────────────────────────────────────────────

class CrossPageAnalyzer:
    """
    Analyzes multiple converted pages to extract shared content.

    Identifies:
    - Common header content (top of <body>) → header.yaml
    - Common footer content (bottom of <body>) → footer.yaml
    - Repeated ^format definitions → shared format files
    """

    def __init__(self, pages: dict[str, dict[str, Any]]):
        self.pages = pages  # filename → YAML dict
        self.shared_header: list[Any] = []
        self.shared_footer: list[Any] = []
        self.shared_formats: dict[str, dict] = OrderedDict()

    def analyze(self):
        if len(self.pages) < 2:
            return

        self._find_common_header_footer()
        self._find_shared_formats()

    def _find_common_header_footer(self):
        """Extract header: and footer: definitions that are identical across all pages."""
        page_list = list(self.pages.values())

        # Check if all pages have identical header: content
        all_headers = [_yaml_repr(doc.get("header")) for doc in page_list]
        if all_headers[0] and len(set(all_headers)) == 1:
            self.shared_header = page_list[0]["header"]
            for doc in page_list:
                del doc["header"]

        # Check if all pages have identical footer: content
        all_footers = [_yaml_repr(doc.get("footer")) for doc in page_list]
        if all_footers[0] and len(set(all_footers)) == 1:
            self.shared_footer = page_list[0]["footer"]
            for doc in page_list:
                del doc["footer"]

        # Also check for common prefix/suffix within main: content
        all_mains = []
        for doc in page_list:
            main = doc.get("main", [])
            if isinstance(main, list):
                all_mains.append(main)

        if len(all_mains) < 2:
            return

        # Find common prefix at start of main
        prefix_len = 0
        min_len = min(len(m) for m in all_mains)
        for i in range(min_len):
            items = [_yaml_repr(m[i]) for m in all_mains]
            if len(set(items)) == 1:
                prefix_len = i + 1
            else:
                break

        # Find common suffix at end of main
        suffix_len = 0
        for i in range(1, min_len - prefix_len + 1):
            items = [_yaml_repr(m[-i]) for m in all_mains]
            if len(set(items)) == 1:
                suffix_len = i
            else:
                break

        # If there's a shared prefix in main, prepend it to header
        if prefix_len > 0:
            self.shared_header = (self.shared_header or []) + all_mains[0][:prefix_len]
        # If there's a shared suffix in main, append it to footer
        if suffix_len > 0:
            self.shared_footer = all_mains[0][-suffix_len:] + (self.shared_footer or [])

        # Strip shared content from individual pages' main:
        if prefix_len > 0 or suffix_len > 0:
            for doc in page_list:
                main = doc.get("main", [])
                if isinstance(main, list):
                    end = len(main) - suffix_len if suffix_len else len(main)
                    doc["main"] = main[prefix_len:end]

    def _find_shared_formats(self):
        """Find ^format definitions that appear identically in multiple pages."""
        format_usage: dict[str, list[str]] = {}  # repr → [filenames]
        format_defs: dict[str, tuple[str, dict]] = {}  # repr → (name, def)

        for fname, doc in self.pages.items():
            for key, val in list(doc.items()):
                if key.startswith("^"):
                    repr_key = f"{key}={_yaml_repr(val)}"
                    format_usage.setdefault(repr_key, []).append(fname)
                    format_defs[repr_key] = (key, val)

        for repr_key, filenames in format_usage.items():
            if len(filenames) >= 2:
                name, defn = format_defs[repr_key]
                self.shared_formats[name] = defn
                # Remove from individual pages
                for fname in filenames:
                    if name in self.pages[fname]:
                        del self.pages[fname][name]

    def get_header_yaml(self) -> dict | None:
        if not self.shared_header:
            return None
        return {"header": self.shared_header}

    def get_footer_yaml(self) -> dict | None:
        if not self.shared_footer:
            return None
        return {"footer": self.shared_footer}

    def get_shared_formats_yaml(self) -> dict | None:
        if not self.shared_formats:
            return None
        return dict(self.shared_formats)


def _yaml_repr(obj: Any) -> str:
    """Stable string representation for comparison."""
    return yaml_dump(obj)


# ─── Main CLI ───────────────────────────────────────────────────────────

def convert_file(filepath: str) -> tuple[str, dict[str, Any]]:
    """Read and convert a single PHP/HTML file. Returns (basename, yaml_dict)."""
    with open(filepath, "r", encoding="utf-8", errors="replace") as f:
        source = f.read()

    parser = PHPHTMLParser()
    root = parser.parse_file(source)

    basename = os.path.splitext(os.path.basename(filepath))[0]
    converter = PageConverter(filepath)
    doc = converter.convert(root)
    return basename, doc


def write_yaml_file(filepath: str, doc: dict[str, Any]):
    """Write a YAML document to a file."""
    content = yaml_dump(doc)
    os.makedirs(os.path.dirname(filepath) or ".", exist_ok=True)
    with open(filepath, "w", encoding="utf-8") as f:
        f.write(content)
        f.write("\n")


def main():
    ap = argparse.ArgumentParser(
        description="Convert PHP/HTML files to bserver YAML pages.",
        epilog="Examples:\n"
               "  python3 php2yaml.py index.php\n"
               "  python3 php2yaml.py --outdir yaml/ *.php\n"
               "  python3 php2yaml.py --check page.html\n",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("files", nargs="+", help="PHP or HTML files to convert")
    ap.add_argument("--outdir", "-o", default=".", help="Output directory (default: current dir)")
    ap.add_argument("--check", action="store_true", help="Dry run: print YAML to stdout instead of writing files")
    ap.add_argument("--no-shared", action="store_true",
                    help="Skip cross-page analysis (no header/footer/shared format extraction)")
    args = ap.parse_args()

    # Convert all files
    pages: dict[str, dict[str, Any]] = OrderedDict()
    for filepath in args.files:
        if not os.path.isfile(filepath):
            print(f"Warning: {filepath} not found, skipping", file=sys.stderr)
            continue
        try:
            basename, doc = convert_file(filepath)
            pages[basename] = doc
            print(f"  Converted {filepath} → {basename}.yaml", file=sys.stderr)
        except Exception as e:
            print(f"  Error converting {filepath}: {e}", file=sys.stderr)

    if not pages:
        print("No files converted.", file=sys.stderr)
        sys.exit(1)

    # Cross-page analysis (when multiple files)
    extra_files: dict[str, dict] = OrderedDict()
    if len(pages) > 1 and not args.no_shared:
        analyzer = CrossPageAnalyzer(pages)
        analyzer.analyze()

        header = analyzer.get_header_yaml()
        if header:
            extra_files["header"] = header
            print(f"  Extracted shared header → header.yaml", file=sys.stderr)

        footer = analyzer.get_footer_yaml()
        if footer:
            extra_files["footer"] = footer
            print(f"  Extracted shared footer → footer.yaml", file=sys.stderr)

        shared_fmts = analyzer.get_shared_formats_yaml()
        if shared_fmts:
            extra_files["formats"] = shared_fmts
            print(f"  Extracted shared formats → formats.yaml", file=sys.stderr)

    # Output
    all_files = {**extra_files, **pages}
    for name, doc in all_files.items():
        if args.check:
            print(f"\n# ── {name}.yaml ──")
            print(yaml_dump(doc))
        else:
            outpath = os.path.join(args.outdir, f"{name}.yaml")
            write_yaml_file(outpath, doc)
            print(f"  Wrote {outpath}", file=sys.stderr)

    total = len(pages)
    shared = len(extra_files)
    print(f"\nDone: {total} page(s) converted" +
          (f", {shared} shared file(s) extracted" if shared else ""),
          file=sys.stderr)


if __name__ == "__main__":
    main()
