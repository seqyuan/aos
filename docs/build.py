#!/usr/bin/env python3
"""Render README.md into _site/index.html for GitHub Pages."""

from pathlib import Path

import markdown

ROOT = Path(__file__).resolve().parent.parent
README = ROOT / "README.md"
TEMPLATE = ROOT / "docs" / "template.html"
OUT_DIR = ROOT / "_site"


def main() -> None:
    body = markdown.markdown(
        README.read_text(encoding="utf-8"),
        extensions=["tables", "fenced_code", "sane_lists"],
    )
    html = TEMPLATE.read_text(encoding="utf-8").replace("{{CONTENT}}", body)
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    (OUT_DIR / "index.html").write_text(html, encoding="utf-8")
    print(f"wrote {OUT_DIR / 'index.html'}")


if __name__ == "__main__":
    main()
