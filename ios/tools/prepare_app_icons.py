"""Prepare native app-icon catalogs from the existing Red-XAI logo.

This resizes existing artwork for engineering previews; it does not create
store screenshots or change the logo design. Requires macOS's built-in sips.
Run automatically by XcodeGen's preGenCommand.
"""
from __future__ import annotations

import json
import struct
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT.parent / 'foundation' / 'assets' / 'red-xai-96.png'
SLOTS = (
    [('iphone', str(size), scale) for size in (20, 29, 40, 60) for scale in (2, 3)]
    + [('ipad', str(size), scale) for size in (20, 29, 40, 76) for scale in (1, 2)]
    + [('ipad', '83.5', 2), ('ios-marketing', '1024', 1)]
)


def verify_png(path: Path, pixels: int) -> None:
    header = path.read_bytes()[:29]
    if len(header) != 29 or header[:8] != b'\x89PNG\r\n\x1a\n' or header[12:16] != b'IHDR':
        raise ValueError(f'Invalid PNG: {path.name}')
    width, height = struct.unpack('>II', header[16:24])
    if (width, height) != (pixels, pixels) or header[24] != 8 or header[25] != 2:
        raise ValueError(f'Icon must be {pixels}x{pixels} 8-bit RGB without alpha: {path.name}')


def write_json(path: Path, value: dict) -> None:
    text = json.dumps(value, indent=2) + '\n'
    if not path.exists() or path.read_text(encoding='utf-8') != text:
        path.write_text(text, encoding='utf-8')


def main() -> None:
    verify_png(SOURCE, 96)
    if not Path('/usr/bin/sips').is_file():
        raise SystemExit('macOS sips is required to prepare native app icons.')
    for target in ('Database', 'Host'):
        catalog = ROOT / target / 'Assets.xcassets'
        icons = catalog / 'AppIcon.appiconset'
        icons.mkdir(parents=True, exist_ok=True)
        write_json(catalog / 'Contents.json', {'info': {'author': 'xcode', 'version': 1}})
        entries = []
        for idiom, points, scale in SLOTS:
            pixels = int(float(points) * scale)
            filename = f'{idiom}-{points}@{scale}x.png'
            output = icons / filename
            regenerate = not output.exists() or output.stat().st_mtime < SOURCE.stat().st_mtime
            if not regenerate:
                try:
                    verify_png(output, pixels)
                except ValueError:
                    regenerate = True
            if regenerate:
                subprocess.run(['/usr/bin/sips', '--resampleHeightWidth', str(pixels), str(pixels),
                                str(SOURCE), '--out', str(output)], check=True,
                               stdout=subprocess.DEVNULL, timeout=15)
            verify_png(output, pixels)
            entries.append({'idiom': idiom, 'size': f'{points}x{points}',
                            'scale': f'{scale}x', 'filename': filename})
        write_json(icons / 'Contents.json', {'images': entries,
                                            'info': {'author': 'xcode', 'version': 1}})
        print(f'{target}: verified {len(entries)} opaque native icon slots, including 120, 152, 167 and 1024 pixels.')


if __name__ == '__main__':
    main()
