#!/usr/bin/env python3
"""Measure whether xcompositediag_pixmap.png contains the same content twice at
an offset -- the "stale root backing pixmap" signature -- rather than being a
single coherent frame.

Pure stdlib (no PIL on this host): decodes the PNG by hand, downsamples to a
small grayscale grid, then scores every candidate (dx, dy) translation by the
normalized correlation between the image and itself shifted by it. A real
screenshot correlates with itself only at (0, 0); a frame carrying a duplicate
copy of its own content shows a second strong peak at the duplication offset.
"""
import struct
import sys
import zlib

PATH = sys.argv[1] if len(sys.argv) > 1 else "xcompositediag_pixmap.png"


def read_png(path):
    with open(path, "rb") as f:
        data = f.read()
    assert data[:8] == b"\x89PNG\r\n\x1a\n", "not a PNG"
    pos, idat, hdr = 8, [], None
    while pos < len(data):
        (ln,) = struct.unpack(">I", data[pos:pos + 4])
        typ = data[pos + 4:pos + 8]
        body = data[pos + 8:pos + 8 + ln]
        if typ == b"IHDR":
            hdr = struct.unpack(">IIBBBBB", body)
        elif typ == b"IDAT":
            idat.append(body)
        elif typ == b"IEND":
            break
        pos += 12 + ln
    w, h, depth, ctype, comp, filt, interlace = hdr
    assert depth == 8 and interlace == 0, f"unsupported PNG ({depth}bpp, interlace={interlace})"
    nch = {0: 1, 2: 3, 4: 2, 6: 4}[ctype]
    return w, h, nch, zlib.decompress(b"".join(idat))


def unfilter_rows(w, h, nch, raw):
    """Yield each row's unfiltered bytes, one at a time, so the whole 16 MB
    image never has to be materialised as Python objects."""
    stride = w * nch
    prev = bytearray(stride)
    pos = 0
    for _ in range(h):
        ft = raw[pos]
        line = bytearray(raw[pos + 1:pos + 1 + stride])
        pos += 1 + stride
        if ft == 1:
            for i in range(nch, stride):
                line[i] = (line[i] + line[i - nch]) & 0xFF
        elif ft == 2:
            for i in range(stride):
                line[i] = (line[i] + prev[i]) & 0xFF
        elif ft == 3:
            for i in range(stride):
                a = line[i - nch] if i >= nch else 0
                line[i] = (line[i] + ((a + prev[i]) >> 1)) & 0xFF
        elif ft == 4:
            for i in range(stride):
                a = line[i - nch] if i >= nch else 0
                c = prev[i - nch] if i >= nch else 0
                b = prev[i]
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pr = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[i] = (line[i] + pr) & 0xFF
        prev = line
        yield line


def downsample(w, h, nch, raw, cols, rows):
    """Box-filter to a cols x rows grayscale grid of floats."""
    acc = [0.0] * (cols * rows)
    cnt = [0] * (cols * rows)
    for y, line in enumerate(unfilter_rows(w, h, nch, raw)):
        gy = y * rows // h
        base = gy * cols
        for x in range(0, w, 2):  # every other column is plenty at this scale
            p = x * nch
            g = (line[p] * 299 + line[p + 1] * 587 + line[p + 2] * 114) / 1000.0
            i = base + x * cols // w
            acc[i] += g
            cnt[i] += 1
    return [acc[i] / cnt[i] if cnt[i] else 0.0 for i in range(cols * rows)]


def correlate(grid, cols, rows, dx, dy):
    """Normalized cross-correlation of grid with itself shifted by (dx, dy)."""
    xs, ys = [], []
    for y in range(rows):
        y2 = y + dy
        if not (0 <= y2 < rows):
            continue
        for x in range(cols):
            x2 = x + dx
            if not (0 <= x2 < cols):
                continue
            xs.append(grid[y * cols + x])
            ys.append(grid[y2 * cols + x2])
    n = len(xs)
    if n < cols * rows // 8:
        return None, n
    mx, my = sum(xs) / n, sum(ys) / n
    sxy = sxx = syy = 0.0
    for a, b in zip(xs, ys):
        da, db = a - mx, b - my
        sxy += da * db
        sxx += da * da
        syy += db * db
    if sxx <= 0 or syy <= 0:
        return None, n
    return sxy / (sxx * syy) ** 0.5, n


def main():
    w, h, nch, raw = read_png(PATH)
    print(f"{PATH}: {w}x{h}, {nch} channels")
    cols, rows = 192, 54  # 20 px per cell at 3840x1080
    grid = downsample(w, h, nch, raw, cols, rows)
    px_x, px_y = w / cols, h / rows

    peaks = []
    for gy in range(0, rows):
        for gx in range(0, cols):
            if gx == 0 and gy == 0:
                continue
            c, n = correlate(grid, cols, rows, gx, gy)
            if c is not None:
                peaks.append((c, gx, gy, n))
    peaks.sort(reverse=True)

    base, _ = correlate(grid, cols, rows, 0, 0)
    print(f"self-correlation at (0,0): {base:.4f} (sanity check, must be 1.0)")
    print("\ntop translation self-correlations (dx, dy in source pixels):")
    for c, gx, gy, n in peaks[:8]:
        print(f"  ({gx * px_x:7.1f}, {gy * px_y:6.1f})  r = {c:.4f}   over {n} cells")

    best = peaks[0]
    print()
    if best[0] >= 0.5:
        print(f"VERDICT: the frame correlates with itself at r={best[0]:.3f} when shifted by "
              f"({best[1] * px_x:.0f}, {best[2] * px_y:.0f}) px. That is a duplicate copy of the "
              f"same content, not a coherent single frame.")
    else:
        print(f"VERDICT: no strong off-origin self-correlation (best r={best[0]:.3f}). "
              f"The frame does not contain a shifted duplicate of itself.")


if __name__ == "__main__":
    main()
