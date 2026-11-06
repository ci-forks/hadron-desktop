#!/usr/bin/env python3
"""Draw the Hadron wallpapers from theme/palette.toml.

"Three quarks, three orbits": the GRUB background's recipe (a vertical base ->
crust gradient with a faint accent glow above the frame) plus a soft violet
bloom, three tilted orbits that fade along their path like comet trails, a
three-dot "quark" nucleus, a light vignette and triangular dithering so the
dark gradient does not band on 8-bit panels. The "plain" variant is the same
background without the orbits, for the lock and login screens.

The image is a pure function of the palette, the variant and the size: the
dither noise comes from a fixed seed, and the PNG has no timestamp, so two runs
write the same bytes. The Dockerfile runs this at build time (stdlib only, no
network), so an edit to theme/palette.toml reaches the wallpapers too.

    gen-wallpaper.py OUTDIR NAME...

NAME is hadron-{orbits|plain}-{night|day}-WIDTHxHEIGHT; each is written to
OUTDIR/NAME.png. For example:

    python3 tools/gen-wallpaper.py /tmp/wp hadron-orbits-night-1920x1080
"""
import math
import re
import struct
import sys
import tomllib
import zlib
from pathlib import Path

PALETTE = Path(__file__).resolve().parent.parent / "theme" / "palette.toml"
NAME = re.compile(r"hadron-(orbits|plain)-(night|day)-([1-9][0-9]{2,4})x([1-9][0-9]{2,4})")


def rgb(h):
    return tuple(int(h[i:i + 2], 16) for i in (1, 3, 5))


def write_png(path, w, h, rows):
    def chunk(t, d):
        c = struct.pack(">I", len(d)) + t + d
        return c + struct.pack(">I", zlib.crc32(t + d) & 0xFFFFFFFF)
    raw = bytearray()
    for r in rows:
        raw += b"\x00" + r
    with open(path, "wb") as f:
        f.write(b"\x89PNG\r\n\x1a\n")
        f.write(chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 2, 0, 0, 0)))
        f.write(chunk(b"IDAT", zlib.compress(bytes(raw), 6)))
        f.write(chunk(b"IEND", b""))


def draw(P, mode, W, H, plain):
    night = mode == "night"
    top, bot = rgb(P["base"]), rgb(P["crust"])
    acc, glo, vio = rgb(P["accent"]), rgb(P["glow"]), rgb(P["violet"])
    s = H / 1080.0
    glow_k = 0.18 if night else 0.10
    line_a = 0.55 if night else 0.45
    halo_a = 0.16 if night else 0.08

    # Orbits around a nucleus on the right third (keeps the left calm for
    # the first tiled window / desktop icons).
    cx, cy = W * 0.68, H * 0.56
    # (tilt, colour, rider phase, semi-major, semi-minor): three nested,
    # unevenly tilted orbits, not the 60-degree "atom" logo.
    orbits = [(math.radians(-16), acc, 0.5, H * 0.44, H * 0.135),
              (math.radians(21), glo, 2.6, H * 0.33, H * 0.105),
              (math.radians(-58), vio, 4.4, H * 0.23, H * 0.085)]
    lw = 1.25 * s
    halo = 9.0 * s
    quarks = [(cx + 14 * s * math.cos(t), cy + 14 * s * math.sin(t), c)
              for t, c in ((-math.pi / 2, acc), (math.pi / 6, glo), (5 * math.pi / 6, vio))]
    # a "particle" riding each orbit at its trail head
    riders = []
    for th, col, ph, A, B in orbits:
        u, v = A * math.cos(ph), B * math.sin(ph)
        riders.append((cx + u * math.cos(th) - v * math.sin(th),
                       cy + u * math.sin(th) + v * math.cos(th), col))

    gcx, gcy, grad = W * 0.5, -H * 0.15, H * 0.95
    g2x, g2y, g2r = cx, cy, H * 0.55          # soft violet bloom behind nucleus
    reach = max(o[3] for o in orbits) + 3 * halo
    seed = 0x9E3779B9
    rows = []
    for y in range(H):
        ty = y / (H - 1)
        br = top[0] + (bot[0] - top[0]) * ty
        bg_ = top[1] + (bot[1] - top[1]) * ty
        bb = top[2] + (bot[2] - top[2]) * ty
        row = bytearray(W * 3)
        dy0 = y - gcy
        dyc = y - cy
        near_orbit = not plain and abs(dyc) < reach
        for x in range(W):
            r, g, b = br, bg_, bb
            dx = x - gcx
            d = math.sqrt(dx * dx + dy0 * dy0)
            k = 1.0 - d / grad
            if k > 0:
                k = k * k * glow_k
                r += (acc[0] - r) * k; g += (acc[1] - g) * k; b += (acc[2] - b) * k
            ex, ey = x - g2x, y - g2y
            k = 1.0 - math.sqrt(ex * ex + ey * ey) / g2r
            if k > 0:
                k = k * k * k * (0.07 if night else 0.05)
                r += (vio[0] - r) * k; g += (vio[1] - g) * k; b += (vio[2] - b) * k
            if near_orbit and abs(x - cx) < reach:
                for th, col, ph, A, B in orbits:
                    c, sn = math.cos(th), math.sin(th)
                    u = ex * c + ey * sn
                    v = -ex * sn + ey * c
                    f = (u / A) ** 2 + (v / B) ** 2 - 1.0
                    gr = 2.0 * math.sqrt((u / (A * A)) ** 2 + (v / (B * B)) ** 2) + 1e-9
                    dd = abs(f) / gr
                    if dd > 3 * halo:
                        continue
                    # trail: brightest at the rider (angle ph), fading behind it
                    ang = math.atan2(v / B, u / A)
                    t = ((ph - ang) % (2 * math.pi)) / (2 * math.pi)  # 0 at head
                    trail = 0.18 + 0.82 * (1.0 - t) ** 2.2
                    a = (line_a * math.exp(-(dd / lw) ** 2)
                         + halo_a * math.exp(-(dd / halo) ** 2)) * trail
                    r += (col[0] - r) * a; g += (col[1] - g) * a; b += (col[2] - b) * a
                for qx, qy, col in quarks + riders:
                    q = math.hypot(x - qx, y - qy)
                    rad = 3.2 * s
                    if q < 40 * s:
                        a = min(1.0, math.exp(-(q / rad) ** 2) * 0.95
                                + math.exp(-(q / (14 * s)) ** 2) * 0.22)
                        r += (col[0] - r) * a; g += (col[1] - g) * a; b += (col[2] - b) * a
            # vignette
            vx = (x / W - 0.5) * 2; vy = (ty - 0.5) * 2
            v2 = (vx * vx + vy * vy) * (0.10 if night else 0.02)
            r *= 1 - v2; g *= 1 - v2; b *= 1 - v2
            # dither: +/- 0.75 LSB triangular noise from a fixed-seed LCG
            seed = (seed * 1103515245 + 12345) & 0x7FFFFFFF
            n = ((seed >> 8) & 0xFF) / 255.0 - ((seed >> 16) & 0xFF) / 255.0
            i = x * 3
            row[i] = max(0, min(255, int(r + n * 0.75 + 0.5)))
            row[i + 1] = max(0, min(255, int(g + n * 0.75 + 0.5)))
            row[i + 2] = max(0, min(255, int(b + n * 0.75 + 0.5)))
        rows.append(bytes(row))
    return rows


def main():
    if len(sys.argv) < 3:
        sys.exit("usage: gen-wallpaper.py OUTDIR hadron-{orbits|plain}-{night|day}-WIDTHxHEIGHT...")
    out = Path(sys.argv[1])
    with open(PALETTE, "rb") as f:
        pal = tomllib.load(f)
    jobs = []
    for name in sys.argv[2:]:
        m = NAME.fullmatch(name)
        if not m:
            sys.exit(f"gen-wallpaper: {name!r} is not hadron-{{orbits|plain}}-{{night|day}}-WIDTHxHEIGHT")
        jobs.append((name, m.group(1) == "plain", m.group(2), int(m.group(3)), int(m.group(4))))
    out.mkdir(parents=True, exist_ok=True)
    for name, plain, mode, w, h in jobs:
        path = out / f"{name}.png"
        write_png(path, w, h, draw(pal[mode], mode, w, h, plain))
        print(f"wrote {path} ({path.stat().st_size} bytes)")


if __name__ == "__main__":
    main()
