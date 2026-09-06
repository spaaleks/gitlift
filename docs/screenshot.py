#!/usr/bin/env python3
"""Regenerate docs/screenshot.png. Run from the repository root."""

import subprocess

FONT = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
POINTSIZE = 18
CHAR_WIDTH = 11.1
LINE_HEIGHT = 22.33

COLS = 86
PAD_X = 22
CHROME_Y = 24
CHROME_GAP = 30
TOP = CHROME_Y + CHROME_GAP + 12
BOTTOM = 24

ACCENT = "#ec483b"
TEXT = "#e0e0e0"
MUTED = "#9b9b9b"
BACKGROUND = "#16161a"
DOTS = ["#ff5f56", "#ffbd2e", "#27c93f"]

ROWS = [
    ("accent", "╔═╗╦╔╦╗╦  ╦╔═╗╔╦╗"),
    ("accent", "║ ╦║ ║ ║  ║╠╣  ║ "),
    ("accent", "╚═╝╩ ╩ ╩═╝╩╚   ╩ "),
    ("rule", None),
    ("blank", ""),
    ("text", "Gitlift"),
    ("blank", ""),
    ("sel", "  > Create a new repository"),
    ("text", "    Configure an existing repository (18)"),
    ("text", "    Bulk sync settings (18)"),
    ("text", "    Create a template"),
    ("text", "    Providers"),
    ("text", "    Help"),
    ("text", "    Quit"),
    ("blank", ""),
    ("rule", None),
    ("muted", "↑↓ move · enter select · esc back"),
]

COLOURS = {"accent": ACCENT, "text": TEXT, "muted": MUTED, "sel": ACCENT}


def column(index):
    return int(PAD_X + index * CHAR_WIDTH)


def build():
    width = int(COLS * CHAR_WIDTH + PAD_X * 2)
    height = int(TOP + len(ROWS) * LINE_HEIGHT + BOTTOM)

    argv = [
        "magick", "-size", f"{width}x{height}", "xc:none",
        "-fill", BACKGROUND,
        "-draw", f"roundrectangle 0,0 {width - 1},{height - 1} 12,12",
        "-font", FONT, "-pointsize", str(POINTSIZE),
    ]

    for index, colour in enumerate(DOTS):
        x = PAD_X + 2 + index * 20
        argv += ["-fill", colour, "-draw", f"circle {x},{CHROME_Y} {x},{CHROME_Y + 5}"]

    for index, (kind, body) in enumerate(ROWS):
        y = int(TOP + index * LINE_HEIGHT)

        if kind == "blank":
            continue

        if kind == "rule":
            argv += [
                "-stroke", MUTED, "-strokewidth", "1", "-fill", "none",
                "-draw", f"line {PAD_X},{y - 8} {width - PAD_X},{y - 8}",
                "-stroke", "none",
            ]
            continue

        indent = len(body) - len(body.lstrip(" "))
        argv += ["-fill", COLOURS[kind], "-annotate", f"+{column(indent)}+{y}", body.lstrip(" ")]

        if kind == "sel":
            argv += ["-fill", MUTED, "-annotate", f"+{column(31)}+{y}", "from a template"]

        if index == 2:
            meta = "2 providers · 18 repositories"
            argv += ["-fill", MUTED, "-annotate", f"+{column(COLS - len(meta))}+{y}", meta]

    argv.append("docs/screenshot.png")
    subprocess.run(argv, check=True)
    print(f"wrote docs/screenshot.png {width}x{height}")


if __name__ == "__main__":
    build()
