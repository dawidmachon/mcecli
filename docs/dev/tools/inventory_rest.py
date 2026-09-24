#!/usr/bin/env python3
"""Extract all REST endpoints from the sf-docs-scrap corpus."""
import re
import os
from pathlib import Path

BASE = Path(__file__).resolve().parents[2] / "markdown/docs/marketing/marketing-cloud/references"

METHOD_RE = re.compile(r'^# ([A-Z]+) (/[\w:{}\-\./]+)')
SECTION_RE = re.compile(r'^## (.+)')
H1_RE = re.compile(r'^# (.+)')

def extract_from_dir(d: Path):
    results = []
    for md in sorted(d.rglob("*.md")):
        section = md.parent.name
        content = md.read_text(encoding="utf-8", errors="ignore")
        for line in content.splitlines():
            m = METHOD_RE.match(line.strip())
            if m:
                results.append((section, m.group(1), m.group(2)))
    return results

all_endpoints = []
for subdir in sorted(BASE.iterdir()):
    if subdir.is_dir():
        all_endpoints.extend(extract_from_dir(subdir))

# Group by section
from collections import defaultdict
by_section = defaultdict(list)
for section, method, path in all_endpoints:
    by_section[section].append((method, path))

for section in sorted(by_section):
    eps = sorted(by_section[section])
    print(f"\n## {section} ({len(eps)})")
    for method, path in eps:
        print(f"- {method:8s} {path}")
