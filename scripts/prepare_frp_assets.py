"""Mirror original official FRP archives after checking the installer's embedded pins."""
import hashlib
from pathlib import Path
import re
import urllib.request


def pins():
    source = Path('internal/installer/install.go').read_text(encoding='utf-8')
    version = re.search(r'const Version = "([0-9]+\.[0-9]+\.[0-9]+)"', source).group(1)
    hashes = dict(re.findall(r'"(amd64|arm64)": "([0-9a-f]{64})"', source))
    if set(hashes) != {'amd64', 'arm64'}:
        raise ValueError('Official FRP architecture pins missing')
    return version, hashes


def prepare():
    version, hashes = pins()
    root = Path('dist')
    for arch, expected in hashes.items():
        name = f'frp_{version}_linux_{arch}.tar.gz'
        target = root/name
        if target.exists():
            raise ValueError('Refusing to overwrite a release archive')
        with urllib.request.urlopen(f'https://github.com/fatedier/frp/releases/download/v{version}/{name}', timeout=90) as r:
            data = r.read((100 << 20)+1)
        if len(data) > 100 << 20 or hashlib.sha256(data).hexdigest() != expected:
            raise ValueError('Official FRP checksum mismatch')
        target.write_bytes(data)
        print(f'Official FRP {arch}: pinned SHA-256 verified', flush=True)
    (root/'FRP_VERSION').write_text(version+'\n', encoding='ascii')


if __name__ == '__main__':
    prepare()
