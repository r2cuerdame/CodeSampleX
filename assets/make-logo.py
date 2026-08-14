#!/usr/bin/env python3
"""Render the CodeSampleX mark to assets/logo-400.png and assets/favicon-32.png.

The mark is one X (for CodeSampleX) whose upward-opening halves are drawn in
the site accent so they read as a check mark: the X is the name, the check
inside it is the only claim the project makes — a sample is an answer once
its contract has actually been run.

Deterministic: same input, same bytes. Re-run after changing geometry:
    python assets/make-logo.py

Colors are the site palette (internal/web/static/site.css): --bg #0f1317,
--accent #d9a441. The dim strokes are a mix of --bg and --dim so the check
stays the brightest thing in the tile at 32px.
"""

import os
import struct
import zlib

from PIL import Image, ImageDraw

S = 8  # supersample factor; the tile is drawn at 400*S then box-filtered down

BG = (15, 19, 23, 255)  # #0f1317  site --bg
ACCENT = (217, 164, 65, 255)  # #d9a441  site --accent
DIM = (74, 87, 100, 255)  # #4a5764   between --bg and --dim: present, never louder than the check

TILE = 400
RADIUS = 88  # 22% — a square-ish tile keeps its silhouette at 32px
STROKE = 54

# X corners and centre, in 400-space.
TL = (88, 88)
TR = (312, 88)
BL = (88, 312)
BR = (312, 312)
C = (200, 200)
# Start of the check's short arm: half way down the TL->C diagonal, which
# gives the 1:2 arm ratio that reads as a check rather than a V.
KNEE = (144, 144)


def seg(draw, a, b, color, width):
    """A line with round caps (Pillow has no cap style)."""
    draw.line([a, b], fill=color, width=width)
    r = width / 2
    for p in (a, b):
        draw.ellipse([p[0] - r, p[1] - r, p[0] + r, p[1] + r], fill=color)


def render(size):
    n = TILE * S
    img = Image.new("RGBA", (n, n), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    d.rounded_rectangle([0, 0, n - 1, n - 1], radius=RADIUS * S, fill=BG)

    def sc(p):
        return (p[0] * S, p[1] * S)

    w = STROKE * S
    # Dim first: the rest of the X, drawn under the check.
    seg(d, sc(TL), sc(KNEE), DIM, w)
    seg(d, sc(C), sc(BR), DIM, w)
    seg(d, sc(BL), sc(C), DIM, w)
    # The check, on top.
    seg(d, sc(KNEE), sc(C), ACCENT, w)
    seg(d, sc(C), sc(TR), ACCENT, w)

    return img.resize((size, size), Image.LANCZOS)


def ihdr(path):
    """Read width/height/bit depth/colour type straight out of the file."""
    with open(path, "rb") as f:
        raw = f.read(33)
    if raw[:8] != b"\x89PNG\r\n\x1a\n":
        raise SystemExit(f"{path}: not a PNG")
    length, ctype = struct.unpack(">I4s", raw[8:16])
    if ctype != b"IHDR":
        raise SystemExit(f"{path}: first chunk is {ctype!r}, not IHDR")
    w, h, depth, color = struct.unpack(">IIBB", raw[16:26])
    stored = struct.unpack(">I", raw[16 + length : 16 + length + 4])[0]
    calc = zlib.crc32(raw[12 : 16 + length]) & 0xFFFFFFFF
    return w, h, depth, color, stored == calc


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    out = [
        (os.path.join(here, "logo-400.png"), 400),
        (os.path.join(here, "favicon-32.png"), 32),
    ]
    for path, size in out:
        render(size).save(path, "PNG", optimize=True)
        w, h, depth, color, crc_ok = ihdr(path)
        with Image.open(path) as im:
            reopened = im.size
        print(
            f"{os.path.basename(path)}: IHDR {w}x{h} depth={depth} colorType={color} "
            f"crc_ok={crc_ok} reopened={reopened} bytes={os.path.getsize(path)}"
        )


if __name__ == "__main__":
    main()
