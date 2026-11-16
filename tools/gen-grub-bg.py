#!/usr/bin/env python3
"""Generate the Isotope GRUB background image.

Emits an uncompressed 24-bit TGA — GRUB's most reliably decoded image format.
(GRUB's png.mod rejects perfectly valid PNGs with "unsupported format", which
cascades to gfxmenu discarding the whole theme; tga.mod decodes cleanly.) The
picture is the night wallpaper of tools/gen-wallpaper.py in its "grub" layout:
the base -> crust gradient with a faint accent glow up top, and the lime tiles
only along the right edge, and a small Isotope mark at the top centre, above
the title that theme.txt draws. The boot menu sits on the plain background. Colours from theme/palette.toml. Stdlib only, so
no Pillow/ImageMagick in the build. Regenerate after a palette edit with:

    python3 tools/gen-grub-bg.py rootfs/etc/kairos/branding/isotope-theme/background.tga
"""
import importlib.util
import struct
import sys
import tomllib
from pathlib import Path

W, H = 1280, 800

TOOLS = Path(__file__).resolve().parent
_spec = importlib.util.spec_from_file_location("gen_wallpaper", TOOLS / "gen-wallpaper.py")
wallpaper = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(wallpaper)


def build_pixels():
    with open(wallpaper.PALETTE, "rb") as f:
        night = tomllib.load(f)["night"]
    px = bytearray()
    for row in wallpaper.draw(night, "night", W, H, plain=False, layout="grub"):
        for i in range(0, len(row), 3):
            px += bytes([row[i + 2], row[i + 1], row[i]])   # TGA stores BGR
    return bytes(px)


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else "background.tga"
    # TGA header: uncompressed true-colour (type 2), 24-bit, top-left origin (0x20).
    header = (bytes([0, 0, 2, 0, 0, 0, 0, 0])
              + struct.pack("<HH", 0, 0)
              + struct.pack("<HH", W, H)
              + bytes([24, 0x20]))
    with open(out, "wb") as f:
        f.write(header)
        f.write(build_pixels())
    print(f"wrote {out} ({18 + W * H * 3} bytes, {W}x{H})")


if __name__ == "__main__":
    main()
