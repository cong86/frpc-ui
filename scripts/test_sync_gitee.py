import hashlib
import http.server
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

import sync_gitee as mirror


class SourceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='frp-sync-test-')
        self.root = Path(self.temp.name)
        self.work, self.remote = self.root/'work', self.root/'remote.git'
        subprocess.run(['git','init','--bare',str(self.remote)], check=True, capture_output=True)
        subprocess.run(['git','init','--initial-branch=main',str(self.work)], check=True, capture_output=True)
        self.old = Path.cwd()
        os.chdir(self.work)
        self.git('config','user.name','Test')
        self.git('config','user.email','test@example.test')
        Path('source').write_text('initial')
        self.git('add','source')
        self.git('commit','-m','initial')
        self.first = self.git('rev-parse','HEAD')
        self.git('update-ref','refs/remotes/origin/main',self.first)
        self.git('push',str(self.remote),'main')

    def tearDown(self):
        os.chdir(self.old)
        self.temp.cleanup()

    def git(self,*args):
        return subprocess.check_output(['git',*args],stderr=subprocess.DEVNULL,text=True).strip()

    def second(self):
        Path('source').write_text('new version')
        self.git('commit','-am','second')
        second = self.git('rev-parse','HEAD')
        self.git('update-ref','refs/remotes/origin/main',second)
        return second

    def test_fast_forward_and_tag_preserve_remote_branch(self):
        self.git('push',str(self.remote),'HEAD:refs/heads/user-branch')
        second = self.second()
        self.git('tag','v0.1.0')
        self.assertEqual(mirror.sync_code(str(self.remote)),second)
        self.assertEqual(self.git('ls-remote',str(self.remote),'refs/heads/user-branch').split()[0],self.first)
        self.assertEqual(self.git('ls-remote',str(self.remote),'refs/tags/v0.1.0').split()[0],second)

    def test_remote_independent_commit_not_overwritten(self):
        self.git('checkout','-b','independent')
        Path('remote-only').write_text('user change')
        self.git('add','remote-only')
        self.git('commit','-m','remote change')
        remote_sha = self.git('rev-parse','HEAD')
        self.git('push',str(self.remote),'HEAD:main')
        self.git('checkout','main')
        self.second()
        with self.assertRaises(mirror.MirrorError):mirror.sync_code(str(self.remote))
        self.assertEqual(self.git('ls-remote',str(self.remote),'refs/heads/main').split()[0],remote_sha)

    def test_conflicting_tag_rejected_before_main_push(self):
        self.git('tag','v0.1.0')
        self.git('push',str(self.remote),'refs/tags/v0.1.0')
        self.second()
        self.git('tag','-f','v0.1.0')
        with self.assertRaises(mirror.MirrorError):mirror.sync_code(str(self.remote))
        self.assertEqual(self.git('ls-remote',str(self.remote),'refs/heads/main').split()[0],self.first)


class ManifestTests(unittest.TestCase):
    def test_mirror_preserves_manifest_and_excludes_rejected_frp(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);tag='v0.1.0-preview.4'
            names={f'frp-console_{tag}_linux_{a}.tar.gz' for a in ('amd64','arm64')} | {f'frp_0.71.0_linux_{a}.tar.gz' for a in ('amd64','arm64')} | {'FRP_VERSION','install.sh','BUILD_INFO.txt'}
            for name in names:(root/name).write_bytes(b'0.71.0\n' if name=='FRP_VERSION' else name.encode())
            manifest=''.join(hashlib.sha256((root/name).read_bytes()).hexdigest()+'  '+name+'\n' for name in sorted(names))
            (root/'SHA256SUMS').write_bytes(manifest.encode())
            uploaded=[]
            def api(path,method='GET',value=None,multipart=None):
                if method=='GET':
                    self.assertEqual(path,'/releases/tags/'+tag)
                    raise mirror.MirrorError('Not found',http_status=404)
                if path=='/releases':return {'id':123,'assets':[]}
                name,content=multipart;uploaded.append((name,content));return {'name':name}
            with patch.object(mirror,'api',side_effect=api):self.assertEqual(mirror.sync_assets(tag,root),6)
            self.assertEqual(uploaded[-1],('SHA256SUMS',manifest.encode()))
            self.assertFalse(any(name.startswith('frp_') for name,_ in uploaded))
            self.assertEqual({name for name,_ in uploaded},names-{f'frp_0.71.0_linux_{a}.tar.gz' for a in ('amd64','arm64')} | {'SHA256SUMS'})

    def test_hash_mismatch_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'file').write_bytes(b'tampered')
            (root/'SHA256SUMS').write_text(hashlib.sha256(b'original').hexdigest()+'  file\n')
            with self.assertRaises(mirror.MirrorError):mirror.checksums(root)

    def test_traversal_rejected(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'SHA256SUMS').write_text('0'*64+'  ../outside\n')
            with self.assertRaises(mirror.MirrorError):mirror.checksums(root)

    def test_incomplete_release_rejected_without_api_write(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'FRP_VERSION').write_text('0.71.0\n')
            (root/'SHA256SUMS').write_text(hashlib.sha256((root/'FRP_VERSION').read_bytes()).hexdigest()+'  FRP_VERSION\n')
            with patch.object(mirror,'api') as api:
                with self.assertRaises(mirror.MirrorError):mirror.sync_assets('v0.1.0',root)
                api.assert_not_called()


class APITests(unittest.TestCase):
    def test_credential_owner_is_verified(self):
        with patch.object(mirror,'api',return_value={'login':'wangcong886'}):mirror.check_auth()
        with patch.object(mirror,'api',return_value={'login':'another-owner'}):
            with self.assertRaises(mirror.MirrorError):mirror.check_auth()

    def test_native_tag_must_match_without_source_writes(self):
        commit='a'*40
        with patch.object(mirror,'api',return_value=[{'name':'v0.1.0','commit':{'sha':commit}}]) as api,patch.object(mirror,'git') as git:
            mirror.wait_mirror_tag('v0.1.0',commit,timeout=0);git.assert_not_called()
            api.assert_called_once_with('/tags?per_page=100&page=1')
        with patch.object(mirror,'api',return_value=[{'name':'v0.1.0','commit':{'sha':'b'*40}}]):
            with self.assertRaises(mirror.MirrorError):mirror.wait_mirror_tag('v0.1.0',commit,timeout=0)
        with patch.object(mirror,'api',return_value=[]):
            with self.assertRaises(mirror.MirrorError):mirror.wait_mirror_tag('v0.1.0',commit,timeout=0)

    def test_token_is_header_only_and_redirect_not_followed(self):
        observed=[]
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                observed.append((self.path,self.headers.get('Authorization')))
                if self.path.endswith('/redirect'):
                    self.send_response(302);self.send_header('Location','/leak');self.end_headers()
                else:
                    self.send_response(403);self.end_headers();self.wfile.write(b'dummy-secret')
            def log_message(self,*args):pass
        with http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler) as server:
            worker=threading.Thread(target=server.serve_forever,daemon=True);worker.start()
            with patch.object(mirror,'API',f'http://127.0.0.1:{server.server_port}'),patch.dict(os.environ,GITEE_TOKEN='dummy-secret'):
                with self.assertRaises(mirror.MirrorError) as error:mirror.api('/denied')
                self.assertNotIn('dummy-secret',str(error.exception))
                with self.assertRaises(mirror.MirrorError):mirror.api('/redirect')
            server.shutdown();worker.join()
        self.assertEqual([x[0] for x in observed],['/denied','/redirect'])
        self.assertTrue(all(x[1]=='Bearer dummy-secret' for x in observed))


if __name__=='__main__':unittest.main()
