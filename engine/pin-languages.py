#!/usr/bin/env python3
"""Regenerate the PIVOT_LANGS table pinned in pins.sh.

Every pivot language ships Opus-MT models en<->X in both directions plus one
Piper voice; through the English hub (internal/providers/pivot) that yields
any-to-any translation without an N^2 matrix of direct pairs. This tool derives
the exact upstream artifacts, content-hashes them, and prints the table rows
that pins.sh feeds to packs.sh. It is the provenance record for those pins: run
it to add a language or refresh a checksum, then paste the output into pins.sh.

    engine/pin-languages.py                 # hash everything, print the table
    engine/pin-languages.py --cache m.jsonl # reuse a prior run's hashes

Model zips come from the Tatoeba-MT bucket (S3 listing, ISO 639-3 dirs); the
picker prefers the same opus / opus+bt formats already shipping for it<->en,
never the differently-laid-out opusTCv releases. Voices come from the
rhasspy/piper-voices index, best available quality first.
"""

import argparse
import hashlib
import json
import re
import sys
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed

# The canonical pivot set: ISO 639-1 tag -> ISO 639-3 (Tatoeba directory) code.
# Every entry has Opus-MT models in both directions and a Piper voice; ja/ko/th
# and other major languages are absent only because Piper ships no voice for
# them, so they cannot be dub targets.
LANGS = {
    "ar": "ara", "bg": "bul", "ca": "cat", "cs": "ces", "da": "dan",
    "de": "deu", "el": "ell", "es": "spa", "fa": "fas", "fi": "fin",
    "fr": "fra", "hi": "hin", "hu": "hun", "is": "isl", "lv": "lav",
    "nl": "nld", "pl": "pol", "pt": "por", "ro": "ron", "ru": "rus",
    "sl": "slv", "sv": "swe", "sw": "swa", "tr": "tur", "uk": "ukr",
    "vi": "vie", "zh": "zho",
}

TATOEBA = "https://object.pouta.csc.fi/Tatoeba-MT-models"
PIPER = "https://huggingface.co/rhasspy/piper-voices/resolve/main"


def fetch(url: str) -> bytes:
    with urllib.request.urlopen(url, timeout=40) as resp:
        return resp.read()


def pick_model(pair: str) -> str:
    """Return the best model-zip URL for a Tatoeba language pair."""
    xml = fetch(f"{TATOEBA}/?prefix={pair}/&delimiter=/").decode()
    zips = [
        k for k in re.findall(r"<Key>([^<]+)</Key>", xml)
        if k.endswith(".zip") and not any(x in k for x in (".eval", ".test", ".data"))
    ]
    if not zips:
        raise LookupError(f"no model zip for {pair}")

    def rank(key: str):
        base = key.split("/")[-1]
        if base.startswith("opus+bt"):
            return (0, base)
        if base.startswith("opus") and "TC" not in base:
            return (1, base)
        if base.startswith("opusTCv") and "+bt" in base:
            return (2, base)
        return (3, base)

    return f"{TATOEBA}/{min(zips, key=rank)}"


def pick_voice(voices: dict, iso1: str):
    """Return (name, dir) of the best Piper voice for a language."""
    order = {"medium": 0, "low": 1, "high": 2, "x_low": 3}
    matches = [
        (order.get(v.get("quality"), 9), name, v)
        for name, v in voices.items()
        if v.get("language", {}).get("code", "").split("_")[0] == iso1
        or v.get("language", {}).get("family") == iso1
    ]
    if not matches:
        raise LookupError(f"no Piper voice for {iso1}")
    _, name, meta = min(matches, key=lambda m: (m[0], m[1]))
    onnx = next(f for f in meta["files"] if f.endswith(".onnx"))
    return name, "/".join(onnx.split("/")[:-1])


def sha256(url: str) -> str:
    digest = hashlib.sha256()
    with urllib.request.urlopen(url, timeout=180) as resp:
        for chunk in iter(lambda: resp.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def resolve(iso1: str, iso3: str, voices: dict) -> dict:
    enx = pick_model(f"eng-{iso3}")
    xen = pick_model(f"{iso3}-eng")
    voice, vdir = pick_voice(voices, iso1)
    return {
        "iso1": iso1, "iso3": iso3, "voice": voice, "vdir": vdir,
        "enx_url": enx, "enx_sha": sha256(enx),
        "xen_url": xen, "xen_sha": sha256(xen),
        "onnx_sha": sha256(f"{PIPER}/{vdir}/{voice}.onnx"),
        "json_sha": sha256(f"{PIPER}/{vdir}/{voice}.onnx.json"),
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--cache", help="JSONL of prior resolve() rows to reuse")
    args = ap.parse_args()

    rows: dict[str, dict] = {}
    if args.cache:
        with open(args.cache, encoding="utf-8") as fh:
            for line in fh:
                if line.strip():
                    row = json.loads(line)
                    rows[row["iso1"]] = row

    todo = [k for k in LANGS if k not in rows]
    if todo:
        voices = json.loads(fetch(f"{PIPER}/voices.json").decode())
        print(f"hashing {len(todo)} language(s)...", file=sys.stderr)
        with ThreadPoolExecutor(max_workers=6) as pool:
            futures = {pool.submit(resolve, k, LANGS[k], voices): k for k in todo}
            for future in as_completed(futures):
                row = future.result()
                rows[row["iso1"]] = row
                print(f"  {row['iso1']}: {row['voice']}", file=sys.stderr)

    for iso1 in sorted(LANGS):
        r = rows[iso1]
        print(" ".join((
            r["iso1"], r["iso3"], r["voice"], r["vdir"],
            r["enx_url"], r["enx_sha"], r["xen_url"], r["xen_sha"],
            r["onnx_sha"], r["json_sha"],
        )))
    return 0


if __name__ == "__main__":
    sys.exit(main())
