"""Fast-forward source/tag synchronization and immutable release attachment mirroring."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.request
import uuid

REPOSITORY = 'wangcong886/frpc-ui'
GIT_URL = 'https://gitee.com/'+REPOSITORY+'.git'
API = 'https://gitee.com/api/v5/repos/'+REPOSITORY
TAG = re.compile(r'^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?$')


class MirrorError(Exception):
    def __init__(self, message, http_status=None):
        super().__init__(message)
        self.http_status = http_status


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        fp.close()
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


def api(path, method='GET', value=None, multipart=None, identity=False):
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
    url = 'https://gitee.com/api/v5/user' if identity else API+path
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.build_opener(NoRedirect()).open(request, timeout=120) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        error.close()
        raise MirrorError(f'Gitee API {method} failed (HTTP {error.code}); response suppressed', http_status=error.code) from None
    except (OSError, ValueError) as error:
        raise MirrorError(f'Gitee API {method} request failed ({type(error).__name__}); inspect remote state before retrying a write') from None


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


def check_auth():
    user = api('', identity=True)
    if not isinstance(user, dict) or user.get('login') != 'wangcong886':
        raise MirrorError('Gitee credential identity did not match the configured repository owner')


def wait_mirror_tag(tag, commit, timeout=600):
    if not TAG.fullmatch(tag) or not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise MirrorError('Invalid expected native mirror tag or commit')
    deadline = time.monotonic()+timeout
    while True:
        for page in range(1, 11):
            rows = api(f'/tags?per_page=100&page={page}')
            if not isinstance(rows, list):
                raise MirrorError('Invalid Gitee tag list')
            found = next((x for x in rows if x.get('name') == tag), None)
            if found:
                if found.get('commit', {}).get('sha') != commit:
                    raise MirrorError('Gitee tag points to another commit; refusing attachment publication')
                return
            if len(rows) < 100:
                break
        remaining = deadline-time.monotonic()
        if remaining <= 0:
            raise MirrorError('Native Gitee mirror tag not ready; inspect the Pull mirror before rerunning')
        print('Waiting for native Gitee mirror tag; no source push performed', flush=True)
        time.sleep(min(15, remaining))


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


def sync_assets(tag, root, manifest_marker=False):
    if not TAG.fullmatch(tag):
        raise MirrorError('Invalid release tag')
    hashes = checksums(root)
    version = (root/'FRP_VERSION').read_text(encoding='ascii').strip()
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise MirrorError('Invalid official FRP version metadata')
    required = {f'frp-console_{tag}_linux_{a}.tar.gz' for a in ('amd64', 'arm64')} | {f'frp_{version}_linux_{a}.tar.gz' for a in ('amd64', 'arm64')} | {'FRP_VERSION', 'install.sh', 'BUILD_INFO.txt'}
    if set(hashes) != required:
        raise MirrorError('Complete Console/official FRP release assets required')
    try:
        release = api('/releases/tags/'+tag)
    except MirrorError as error:
        if error.http_status != 404:
            raise
        release = None
    if release is None:
        release = api('/releases', 'POST', {'tag_name':tag, 'target_commitish':tag, 'name':'FRP Console '+tag, 'prerelease':True, 'body':'GitHub Console preview mirror. Gitee rejects original official FRP archives; obtain FRP from official GitHub or supply a verified local archive with --frp-archive. SHA256SUMS remains the original complete GitHub manifest. This mirror contains Console, FRP_VERSION, BUILD_INFO.txt, install.sh and SHA256SUMS; FRP archives are not hosted here.'})
    if not isinstance(release, dict) or release.get('tag_name', tag) != tag:
        raise MirrorError('Invalid release response')
    release_id = release.get('id')
    if not isinstance(release_id, int):
        raise MirrorError('Release ID unavailable')
    assets = release.get('assets') or []
    if not isinstance(assets, list):
        raise MirrorError('Invalid release attachment list')
    existing = {x['name'] for x in assets if isinstance(x, dict) and 'name' in x}
    # Gitee's scanner rejects the original official FRP binaries. Do not alter or
    # split them to bypass that restriction. Keep the original manifest intact.
    mirrored = required - {f'frp_{version}_linux_{a}.tar.gz' for a in ('amd64', 'arm64')}
    # SHA256SUMS is uploaded after every supported mirror attachment.
    hashes['SHA256SUMS'] = hashlib.sha256((root/'SHA256SUMS').read_bytes()).hexdigest()
    if manifest_marker and 'SHA256SUMS' in existing:
        if not (mirrored | {'SHA256SUMS'}) <= existing:
            raise MirrorError('Published readiness marker exists but mirror attachments are incomplete')
        if public_asset_hash(tag, 'SHA256SUMS') != hashes['SHA256SUMS']:
            raise MirrorError('Published readiness marker differs; refusing to overwrite the release')
        print('Gitee mirror already published: complete attachment names and original manifest marker match; archive content audit not performed', flush=True)
        return len(mirrored)+1
    for name in sorted(mirrored)+['SHA256SUMS']:
        if name in existing:
            if public_asset_hash(tag, name) != hashes[name]:
                raise MirrorError('Existing attachment differs; published assets are never overwritten')
            print(f'Gitee attachment {name}: already matches', flush=True)
        else:
            response = api(f'/releases/{release_id}/attach_files', 'POST', multipart=(name, (root/name).read_bytes()))
            if response.get('name') != name:
                raise MirrorError('Uploaded attachment name did not match')
            print(f'Gitee attachment {name}: uploaded', flush=True)
    return len(mirrored)+1


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--code', action='store_true')
    parser.add_argument('--check-auth', action='store_true')
    parser.add_argument('--wait-tag')
    parser.add_argument('--commit')
    parser.add_argument('--tag')
    parser.add_argument('--assets-dir', type=Path)
    parser.add_argument('--manifest-marker', action='store_true', help='Trust the final published manifest for completed mirrors; does not audit existing archive bytes')
    args = parser.parse_args()
    if not os.environ.get('GITEE_TOKEN'):
        raise MirrorError('Configure repository Actions secret GITEE_TOKEN first')
    os.environ['GIT_ASKPASS'] = str(Path(__file__).with_name('gitee-askpass.sh').resolve())
    os.environ['GIT_TERMINAL_PROMPT'] = '0'
    if args.check_auth:
        check_auth()
        print('Gitee credential identity verified', flush=True)
    if args.wait_tag and args.commit:
        wait_mirror_tag(args.wait_tag, args.commit)
        print('Native Gitee tag matches the GitHub release commit', flush=True)
    elif args.wait_tag or args.commit:
        raise MirrorError('--wait-tag and --commit are required together')
    if args.code:
        print('Gitee main verified: '+sync_code(), flush=True)
    if args.tag and args.assets_dir:
        print('Gitee mirrored attachment count: '+str(sync_assets(args.tag, args.assets_dir, manifest_marker=args.manifest_marker)), flush=True)
    elif args.tag or args.assets_dir:
        raise MirrorError('--tag and --assets-dir are required together')


if __name__ == '__main__':
    try:
        main()
    except MirrorError as error:
        raise SystemExit(str(error))
