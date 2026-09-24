#!/usr/bin/env python3
"""inventory_soap.py — extract the SOAP object×method support matrix.

Parses supported_operations_for_objects_and_methods.md from the corpus and
emits: every object, its supported methods, and a summary.
"""
import sys
from pathlib import Path

CORPUS = Path(sys.argv[1] if len(sys.argv) > 1 else Path.home() / "Documents/git-clones/sf-docs-scrap")
SRC = CORPUS / "markdown/docs/marketing/marketing-cloud/guide/supported_operations_for_objects_and_methods.md"

METHODS = ["Create", "Retrieve", "Update", "Delete", "Perform", "Schedule", "Configure"]

def main() -> None:
    rows = []
    for line in SRC.read_text(encoding="utf-8", errors="replace").splitlines():
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if len(cells) < 8 or cells[0] in ("APIObject", "---"):
            continue
        obj = cells[0]
        if obj.startswith("-"):
            continue
        supported = [m for m, c in zip(METHODS, cells[1:8]) if "X" in c]
        rows.append((obj, supported))
    print(f"TOTAL SOAP objects in matrix: {len(rows)}\n")
    ret = [o for o, s in rows if "Retrieve" in s]
    print(f"Retrieve-capable ({len(ret)}):")
    print("  " + ", ".join(ret))
    print()
    for obj, sup in rows:
        print(f"- {obj}: {', '.join(sup) if sup else '(none)'}")

if __name__ == "__main__":
    main()
