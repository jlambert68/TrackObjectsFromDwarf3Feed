#!/usr/bin/env python3

from __future__ import annotations

from collections import Counter
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parent
GUIDS_PATH = ROOT / "guids.txt"
DUPLICATES_PATH = ROOT / "duplicates_guids.txt"
GO_GLOB = "*.go"
GUID_PATTERN = re.compile(
    r"\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b"
)


def clear_output_files() -> None:
    GUIDS_PATH.write_text("", encoding="utf-8")
    DUPLICATES_PATH.write_text("", encoding="utf-8")


def find_guids() -> Counter[str]:
    counts: Counter[str] = Counter()
    for path in ROOT.rglob(GO_GLOB):
        text = path.read_text(encoding="utf-8", errors="ignore")
        for match in GUID_PATTERN.findall(text):
            counts[match.lower()] += 1
    return counts


def write_guid_outputs(counts: Counter[str]) -> None:
    unique_guids = sorted(counts)
    duplicate_guids = sorted(guid for guid, count in counts.items() if count > 1)

    GUIDS_PATH.write_text(
        "\n".join(unique_guids) + ("\n" if unique_guids else ""),
        encoding="utf-8",
    )
    DUPLICATES_PATH.write_text(
        "\n".join(f"{guid} count={counts[guid]}" for guid in duplicate_guids)
        + ("\n" if duplicate_guids else ""),
        encoding="utf-8",
    )


def main() -> None:
    clear_output_files()
    counts = find_guids()
    write_guid_outputs(counts)


if __name__ == "__main__":
    main()
