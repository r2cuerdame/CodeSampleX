# Brand assets

## The mark

One X — for CodeSampleX — whose two upward halves are drawn in the site accent
so that the bright part of the letter is a check mark: the X is the name, and
the check inside it is the only claim the project makes, that a sample is an
answer once its contract has actually been run.

Everything is drawn from primitives (rounded rectangle, five stroked segments)
in [`make-logo.py`](make-logo.py); no third-party mark, typeface or clip art is
involved. Colors are the site palette from `internal/web/static/site.css`:
background `#0f1317` (`--bg`), check `#d9a441` (`--accent`), and the remaining
strokes `#4a5764`, sitting between `--bg` and `--dim` so they read as the rest
of the letter without ever competing with the check.

| File | Size | Use |
|---|---|---|
| `logo-400.png` | 400x400 | directory and registry listings that require a square logo |
| `favicon-32.png` | 32x32 | favicon / small avatar |

Both are RGBA PNGs, rendered at 8x and box-filtered down so the diagonals stay
clean at 32 px. The tile is rounded, so the four corner pixels are fully
transparent; if a directory demands a fully opaque image, composite over
`#0f1317`.

## Regenerating

```sh
python assets/make-logo.py
```

Requires Pillow. The script prints the IHDR it reads back out of each file it
wrote — dimensions, bit depth, color type, CRC check — so a regeneration that
silently produced the wrong size is visible in its own output:

```
logo-400.png: IHDR 400x400 depth=8 colorType=6 crc_ok=True reopened=(400, 400) bytes=14481
favicon-32.png: IHDR 32x32 depth=8 colorType=6 crc_ok=True reopened=(32, 32) bytes=1462
```

The render is deterministic: unchanged geometry produces identical bytes.

## License

Same as the rest of the repository (Apache-2.0). The mark identifies the
project; please do not use it as your own project's logo.
