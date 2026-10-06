"""Fast-forward source/tag synchronization and immutable release attachment mirroring."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.request
import uuid

REPOSITORY = 'wangcong886/frpc-ui'
GIT_URL = 'https://gitee.com/'+REPOSITORY+'.git'
API = 'https://gitee.com/api/v5/repos/'+REPOSITORY
TAG = re.compile(r'^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?$')


class MirrorError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise MirrorError('Authenticated API redirect refused')


def git(*args, allow_failure=False):
    result = subprocess.run(['git', *args], capture_output=True, text=True)
    if result.returncode and not allow_failure:
        raise MirrorError('Git synchronization step failed; no force push performed')
    return result


def sync_code(target=GIT_URL):
    source = git('rev-parse', 'refs/remotes/origin/main').stdout.strip()
    existing = git('ls-remote', target, 'refs/heads/main').stdout.split()
    if existing:
        git('fetch', '--no-tags', target, 'refs/heads/main:refs/remotes/gitee-sync/main')
        if git('merge-base', '--is-ancestor', 'refs/remotes/gitee-sync/main', source, allow_failure=True).returncode:
            raise MirrorError('Gitee main contains independent changes; merge them before synchronization')
    remote_tags = {}
    for line in git('ls-remote', '--tags', target).stdout.splitlines():
        digest, ref = line.split()
        if not ref.endswith('^{}'):
            remote_tags[ref] = digest
    for line in git('for-each-ref', '--format=%(objectname) %(refname)', 'refs/tags').stdout.splitlines():
        digest, ref = line.split()
        if ref in remote_tags and remote_tags[ref] != digest:
            raise MirrorError('Gitee has a different tag identity; refusing to replace it')
    # Ordinary pushes preserve remote-only branches/tags and reject changed tag identities.
    git('push', target, source+':refs/heads/main')
    git('push', target, '--tags')
    observed = git('ls-remote', target, 'refs/heads/main').stdout.split()
    if not observed or observed[0] != source:
        raise MirrorError('Gitee main verification did not match GitHub main')
    return source


def api(path, method='GET', value=None, multipart=None):
    token = os.environ.get('GITEE_TOKEN', '')
    if not token:
        raise MirrorError('Configure repository Actions secret GITEE_TOKEN first')
    headers = {'Authorization': 'Bearer '+token, 'Accept': 'application/json'}
    data = None
    if value is not None:
        data = json.dumps(value).encode()
        headers['Content-Type'] = 'application/json'
    if multipart is not None:
        name, content = multipart
        boundary = 'frp-console-'+uuid.uuid4().hex
        data = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{name}"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode()+content+f'\r\n--{boundary}--\r\n'.encode())
        headers['Content-Type'] = 'multipart/form-data; boundary='+boundary
    request = urllib.request.Request(API+path, data=data, headers=headers, method=method)
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=120) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise MirrorError(f'Gitee API {method} failed (HTTP {error.code}); response suppressed') from None
    except (OSError, ValueError):
        raise MirrorError('Gitee API request failed; inspect remote state before retrying a write') from None


def checksums(root):
    result = {}
    for line in (root/'SHA256SUMS').read_text(encoding='ascii').splitlines():
        fields = line.split()
        if len(fields) != 2:
            raise MirrorError('Invalid release checksum manifest')
        digest, name = fields
        if not re.fullmatch(r'[0-9a-f]{64}', digest) or not re.fullmatch(r'[A-Za-z0-9_.-]+', name) or name in ('.', '..') or name in result:
            raise MirrorError('Unsafe or duplicate release checksum entry')
        path = root/name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 100 << 20 or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise MirrorError('Release asset does not match SHA256SUMS')
        result[name] = digest
    return result


def public_asset_hash(tag, name):
    url = f'https://gitee.com/{REPOSITORY}/releases/download/{tag}/{name}'
    try:
        with urllib.request.urlopen(url, timeout=90) as response:
            data = response.read((100 << 20)+1)
        if len(data) > 100 << 20:
            raise MirrorError('Existing attachment exceeds the release size bound')
        return hashlib.sha256(data).hexdigest()
    except OSError:
        raise MirrorError('Existing public attachment unavailable; refusing to replace it') from None


def sync_assets(tag, root):
    if not TAG.fullmatch(tag):
        raise MirrorError('Invalid release tag')
    hashes = checksums(root)
    version = (root/'FRP_VERSION').read_text(encoding='ascii').strip()
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise MirrorError('Invalid official FRP version metadata')
    required = {f'frp-console_{tag}_linux_{a}.tar.gz' for a in ('amd64', 'arm64')} | {f'frp_{version}_linux_{a}.tar.gz' for a in ('amd64', 'arm64')} | {'FRP_VERSION', 'install.sh', 'BUILD_INFO.txt'}
    if set(hashes) != required:
        raise MirrorError('Complete Console/official FRP release assets required')
    release = None
    for page in range(1, 11):
        rows = api(f'/releases?per_page=100&page={page}')
        if not isinstance(rows, list):
            raise MirrorError('Invalid release list response')
        release = next((x for x in rows if x.get('tag_name') == tag), None)
        if release or len(rows) < 100:
            break
    if release is None:
        release = api('/releases', 'POST', {'tag_name':tag, 'target_commitish':tag, 'name':'FRP Console '+tag, 'prerelease':True, 'body':'GitHub preview mirror. Original official FRP archives are included. SHA256SUMS is uploaded last; use this release only when all listed files are available.'})
    release_id = release.get('id')
    if not isinstance(release_id, int):
        raise MirrorError('Release ID unavailable')
    assets = release.get('assets') or []
    if not isinstance(assets, list):
        raise MirrorError('Invalid release attachment list')
    existing = {x['name'] for x in assets if isinstance(x, dict) and 'name' in x}
    # SHA256SUMS is the readiness marker: do not advertise it before every other file.
    hashes['SHA256SUMS'] = hashlib.sha256((root/'SHA256SUMS').read_bytes()).hexdigest()
    for name in sorted(required)+['SHA256SUMS']:
        if name in existing:
            if public_asset_hash(tag, name) != hashes[name]:
                raise MirrorError('Existing attachment differs; published assets are never overwritten')
            print(f'Gitee attachment {name}: already matches', flush=True)
        else:
            response = api(f'/releases/{release_id}/attach_files', 'POST', multipart=(name, (root/name).read_bytes()))
            if response.get('name') != name:
                raise MirrorError('Uploaded attachment name did not match')
            print(f'Gitee attachment {name}: uploaded', flush=True)
    return len(hashes)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--code', action='store_true')
    parser.add_argument('--tag')
    parser.add_argument('--assets-dir', type=Path)
    args = parser.parse_args()
    if not os.environ.get('GITEE_TOKEN'):
        raise MirrorError('Configure repository Actions secret GITEE_TOKEN first')
    os.environ['GIT_ASKPASS'] = str(Path(__file__).with_name('gitee-askpass.sh').resolve())
    os.environ['GIT_TERMINAL_PROMPT'] = '0'
    if args.code:
        print('Gitee main verified: '+sync_code(), flush=True)
    if args.tag and args.assets_dir:
        print('Gitee mirrored attachment count: '+str(sync_assets(args.tag, args.assets_dir)), flush=True)
    elif args.tag or args.assets_dir:
        raise MirrorError('--tag and --assets-dir are required together')


if __name__ == '__main__':
    try:
        main()
    except MirrorError as error:
        raise SystemExit(str(error))
