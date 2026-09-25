"""Expand the checksummed source transport. No eval, subprocesses or path escapes."""
import base64,hashlib,json,lzma
from pathlib import Path,PurePosixPath
HERE=Path(__file__).resolve().parent
manifest=json.loads((HERE/'manifest.json').read_text())
parts=[HERE/f'part-{i:02d}.b64' for i in range(manifest['parts'])]
packed=base64.b64decode(''.join(p.read_text().strip() for p in parts),validate=True)
if hashlib.sha256(packed).hexdigest()!=manifest['xz_sha256']:raise SystemExit('Source transport SHA256 mismatch')
raw=lzma.decompress(packed)
if len(raw)>4*1024*1024:raise SystemExit('Source bundle exceeds limit')
files=json.loads(raw);root=HERE.parent/'foundation';root.mkdir(exist_ok=True)
for name,content in files.items():
    relative=PurePosixPath(name)
    if relative.is_absolute() or '\\' in name or any(p in ('','..','.') for p in name.split('/')):
        raise SystemExit('Unsafe source path')
    target=root/relative
    if not target.resolve().is_relative_to(root.resolve()):raise SystemExit('Source path escapes destination')
    target.parent.mkdir(parents=True,exist_ok=True)
    if isinstance(content,dict):target.write_bytes(base64.b64decode(content['base64'],validate=True))
    else:target.write_text(content,encoding='utf-8')
print(f'Expanded {len(files)} source files; SHA256 verified; no credentials included.')
