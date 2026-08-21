#!/usr/bin/env python3
"""
Draws the small badges the UI puts beside a service or a process: the language
it is written in, and whether it runs in a container or straight on the host.

The badges are committed PNGs, not generated at build time, so a normal `npm
run build` needs nothing but node. This script is how they are regenerated —
run it after changing a colour or adding a language:

    python3 web/scripts/generate-runtime-icons.py

It writes a one-page PDF per badge and hands it to macOS's `sips` to rasterise,
which is the only image toolchain a stock macOS has: there is no Pillow, no
ImageMagick and no librsvg to depend on. The PDF is assembled by hand for the
same reason.

The marks are deliberately apm2go's own — a letter, or a plain arrangement of
rectangles, on a conventional colour — rather than the projects' logos, which
are trademarks with their own usage terms. Colour does most of the identifying
and the mark settles the rest; the `alt` text carries the full name for anyone
the colour does not reach.
"""

import os
import subprocess
import sys
import tempfile

# The rendered size in pixels. Badges display at 16-20px, so this is roughly 4x,
# which stays sharp on every display without the files being worth optimising.
SIZE = 72

# Helvetica-Bold advance widths, in 1/1000 em, for the characters used below.
# Only these are needed, and hardcoding them beats parsing an AFM that macOS
# does not ship in a stable location.
WIDTHS = {
    "J": 556, "N": 722, "P": 667, "y": 556, "G": 778, "o": 611, "b": 611,
    "R": 722, "s": 556, "C": 722, "#": 556, "?": 611,
    "n": 611, "x": 556, "a": 556, "p": 611,
}


def rounded_rect(x, y, width, height, radius):
    """A rounded rectangle, as PDF path operators."""
    k = radius * 0.5523  # a circular arc, as a cubic bezier
    left, right = x, x + width
    bottom, top = y, y + height
    return (
        f"{left + radius:.2f} {bottom:.2f} m "
        f"{right - radius:.2f} {bottom:.2f} l "
        f"{right - radius + k:.2f} {bottom:.2f} {right:.2f} {bottom + radius - k:.2f} "
        f"{right:.2f} {bottom + radius:.2f} c "
        f"{right:.2f} {top - radius:.2f} l "
        f"{right:.2f} {top - radius + k:.2f} {right - radius + k:.2f} {top:.2f} "
        f"{right - radius:.2f} {top:.2f} c "
        f"{left + radius:.2f} {top:.2f} l "
        f"{left + radius - k:.2f} {top:.2f} {left:.2f} {top - radius + k:.2f} "
        f"{left:.2f} {top - radius:.2f} c "
        f"{left:.2f} {bottom + radius:.2f} l "
        f"{left:.2f} {bottom + radius - k:.2f} {left + radius - k:.2f} {bottom:.2f} "
        f"{left + radius:.2f} {bottom:.2f} c h"
    )


def text_mark(mark):
    """Draws one or two characters, centred, in white."""
    font_size = SIZE * (0.61 if len(mark) == 1 else 0.44)
    text_width = sum(WIDTHS[ch] for ch in mark) / 1000 * font_size
    # Centred on cap height rather than on the full em box: a descender in "Py"
    # should hang below centre, the way it does in any other setting of it.
    cap_height = 0.717 * font_size
    x = (SIZE - text_width) / 2
    y = (SIZE - cap_height) / 2
    return f"1 1 1 rg\nBT /F1 {font_size:.2f} Tf {x:.2f} {y:.2f} Td ({mark}) Tj ET\n"


def shape_mark(shapes):
    """Draws rectangles, each either white or knocked back to the tile colour."""
    out = []
    for x, y, width, height, radius, ink in shapes:
        out.append("1 1 1 rg" if ink == "white" else "TILE_RG")
        out.append(rounded_rect(x, y, width, height, radius) + " f")
    return "\n".join(out) + "\n"


# Every language apm2go can identify: the badge mark, and the colour that
# language's own community uses for it.
RUNTIMES = [
    ("java", text_mark("J"), (0xE7, 0x6F, 0x00)),
    ("nodejs", text_mark("N"), (0x53, 0x9E, 0x43)),
    ("python", text_mark("Py"), (0x37, 0x76, 0xAB)),
    ("go", text_mark("Go"), (0x00, 0x9D, 0xC4)),
    ("php", text_mark("P"), (0x77, 0x7B, 0xB4)),
    ("ruby", text_mark("Rb"), (0xCC, 0x34, 0x2D)),
    ("rust", text_mark("Rs"), (0xB7, 0x41, 0x0E)),
    ("dotnet", text_mark("C#"), (0x51, 0x2B, 0xD4)),
    # Web servers, badged as the software rather than as the C they are written
    # in. "N" already means Node.js, so nginx takes two letters; httpd is badged
    # "ap" because Apache is what everyone calls it, whatever the binary is
    # named on a given distribution.
    ("nginx", text_mark("nx"), (0x00, 0x96, 0x39)),
    ("httpd", text_mark("ap"), (0xD2, 0x21, 0x28)),
    # Shown when telemetry arrived without a language attribute, so "we do not
    # know" is visible as itself rather than as a missing badge that reads like
    # a rendering bug.
    ("unknown", text_mark("?"), (0x6B, 0x72, 0x80)),
]

# Where a process runs. These two are drawn rather than lettered because the
# distinction is spatial: a stack of crates against a rack of hardware reads at
# 16px, where "C" against "H" needs to be decoded and, worse, collides with the
# language letters sitting right next to it.
PLACEMENTS = [
    # Three crates, two below and one centred above.
    ("container", shape_mark([
        (16, 16, 18, 18, 3, "white"),
        (38, 16, 18, 18, 3, "white"),
        (27, 38, 18, 18, 3, "white"),
    ]), (0x24, 0x96, 0xED)),
    # Two rack units, each with an indicator knocked out of it.
    ("host", shape_mark([
        (15, 15, 42, 17, 3, "white"),
        (20, 21, 6, 5, 1, "tile"),
        (15, 40, 42, 17, 3, "white"),
        (20, 46, 6, 5, 1, "tile"),
    ]), (0x64, 0x74, 0x8B)),
]


def pdf(mark, colour):
    """One page carrying the badge, as PDF bytes."""
    red, green, blue = (c / 255 for c in colour)
    tile = f"{red:.3f} {green:.3f} {blue:.3f} rg"

    stream = (
        f"q {tile}\n"
        f"{rounded_rect(0, 0, SIZE, SIZE, SIZE * 0.24)} f\n"
        f"{mark.replace('TILE_RG', tile)}"
        f"Q\n"
    ).encode()

    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "
        b"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>" % (SIZE, SIZE),
        b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"endstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>",
    ]

    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for number, body in enumerate(objects, 1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % number + body + b"\nendobj\n"

    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objects) + 1)
    for offset in offsets:
        out += b"%010d 00000 n \n" % offset
    out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (
        len(objects) + 1,
        xref,
    )
    return bytes(out)


def render(badges, out_dir, tmp):
    os.makedirs(out_dir, exist_ok=True)
    for name, mark, colour in badges:
        source = os.path.join(tmp, f"{name}.pdf")
        with open(source, "wb") as handle:
            handle.write(pdf(mark, colour))

        target = os.path.abspath(os.path.join(out_dir, f"{name}.png"))
        result = subprocess.run(
            ["sips", "-s", "format", "png", source, "--out", target],
            capture_output=True,
        )
        if result.returncode != 0:
            sys.exit(f"sips failed for {name}: {result.stderr.decode()}")
        print(f"wrote {target}")


def main():
    here = os.path.dirname(os.path.abspath(__file__))
    assets = os.path.join(here, "..", "src", "assets")

    with tempfile.TemporaryDirectory() as tmp:
        render(RUNTIMES, os.path.join(assets, "runtimes"), tmp)
        render(PLACEMENTS, os.path.join(assets, "placement"), tmp)


if __name__ == "__main__":
    main()
