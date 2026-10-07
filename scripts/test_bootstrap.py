"""Linux bootstrap contract tests using local artifacts, no network or host changes."""
import hashlib
import io
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import tarfile
import tempfile
import time
import unittest

SCRIPT = Path(__file__).with_name('install.sh').resolve()
VERSION = 'v0.1.0-preview.8'


class BootstrapTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='frp-bootstrap-test-')
        self.root = Path(self.tmp.name)
        self.mock = self.root / 'mock'
        self.mock.mkdir()
        self.marker = self.root / 'executed'
        self.env = dict(os.environ, PATH=str(self.mock)+':'+os.environ['PATH'], FIXTURE=str(self.root), MARKER=str(self.marker))
        self.command('id', 'printf "0\\n"')
        self.command('uname', 'if [[ $1 == -s ]]; then echo Linux; else echo "${TEST_ARCH:-x86_64}"; fi')
        self.command('systemctl', 'echo 252')
        self.command('curl', 'url=""; output=""; while (($#)); do case "$1" in --output) output=$2; shift 2;; https://*) url=$1; shift;; *) shift;; esac; done; cp "$FIXTURE/${url##*/}" "$output"')
        self.archive('amd64')
        self.archive('arm64')
        (self.root/'FRP_VERSION').write_text('0.71.0\n')
        for arch in ('amd64', 'arm64'):
            (self.root/f'frp_0.71.0_linux_{arch}.tar.gz').write_bytes(b'official-archive-fixture')
        self.checksums()

    def tearDown(self):
        self.tmp.cleanup()

    def command(self, name, body):
        f = self.mock / name
        f.write_text('#!/usr/bin/env bash\nset -euo pipefail\n'+body+'\n')
        f.chmod(0o755)

    def archive(self, arch, member='frp-console'):
        body = b'#!/usr/bin/env bash\n[[ -t 0 ]] || exit 42\n[[ "$1 $2" == "${EXPECT_COMMAND:-install wizard}" ]] || exit 43\nprintf "tty-ok" > "$MARKER"\n'
        with tarfile.open(self.root / f'frp-console_{VERSION}_linux_{arch}.tar.gz', 'w:gz') as tar:
            info = tarfile.TarInfo(member)
            info.size, info.mode = len(body), 0o755
            tar.addfile(info, io.BytesIO(body))

    def checksums(self):
        files = sorted(self.root.glob('*.tar.gz'))+[self.root/'FRP_VERSION']
        (self.root/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in files))

    def run_script(self, *args):
        return subprocess.run(['bash', str(SCRIPT), *args], env=self.env, capture_output=True, start_new_session=True)

    def test_both_architectures_verify_without_execution(self):
        for arch in ('x86_64', 'aarch64'):
            self.env['TEST_ARCH'] = arch
            r = self.run_script('--verify-only')
            self.assertEqual(r.returncode, 0, r.stderr.decode())
        self.assertFalse(self.marker.exists())

    def test_corrupt_archive_never_executes(self):
        (self.root/f'frp-console_{VERSION}_linux_amd64.tar.gz').write_bytes(b'tampered')
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('摘要不匹配'.encode(), r.stderr)
        self.assertFalse(self.marker.exists())

    def test_unexpected_archive_member_rejected(self):
        self.archive('amd64', '../frp-console')
        self.checksums()
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('归档包含不符合预期的文件'.encode(), r.stderr)

    def test_missing_checksum_rejected(self):
        (self.root/'SHA256SUMS').write_text('')
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('发布清单缺少所需摘要'.encode(), r.stderr)

    def test_unsupported_architecture_rejected(self):
        self.env['TEST_ARCH'] = 'mips'
        self.assertNotEqual(self.run_script('--verify-only').returncode, 0)

    def test_unsafe_version_rejected(self):
        self.assertNotEqual(self.run_script('--verify-only', '--version', '../../other').returncode, 0)

    def test_github_source_remains_available(self):
        self.assertEqual(self.run_script('--verify-only', '--source', 'github').returncode, 0)

    def test_unknown_source_rejected(self):
        self.assertNotEqual(self.run_script('--verify-only', '--source', 'third-party').returncode, 0)

    def test_invalid_deployment_mode_rejected(self):
        self.assertNotEqual(self.run_script('--verify-only','--mode','anything'),0)

    def test_adoption_downloads_console_only(self):
        self.command('curl', 'url=""; output=""; while (($#)); do case "$1" in --output) output=$2; shift 2;; https://*) url=$1; shift;; *) shift;; esac; done; [[ $url != */FRP_VERSION && $url != */frp_0.71.0_* ]] || exit 77; cp "$FIXTURE/${url##*/}" "$output"')
        result=self.run_script('--mode','adopt','--verify-only')
        self.assertEqual(result.returncode,0,result.stderr.decode())
        self.assertFalse(self.marker.exists())
        self.assertIn('未下载官方 FRP'.encode(),result.stdout)

    def test_corrupt_frp_archive_never_executes(self):
        (self.root/'frp_0.71.0_linux_amd64.tar.gz').write_bytes(b'tampered')
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('摘要不匹配'.encode(), r.stderr)
        self.assertFalse(self.marker.exists())

    def test_local_frp_archive_avoids_github_and_checks_digest(self):
        self.command('curl', 'url=""; output=""; while (($#)); do case "$1" in --output) output=$2; shift 2;; https://*) url=$1; shift;; *) shift;; esac; done; [[ $url == https://gitee.com/* && $url != */frp_0.71.0_* ]] || exit 77; cp "$FIXTURE/${url##*/}" "$output"')
        archive=self.root/'frp_0.71.0_linux_amd64.tar.gz'
        result=self.run_script('--verify-only','--frp-archive',str(archive))
        self.assertEqual(result.returncode,0,result.stderr.decode())
        archive.write_bytes(b'tampered-local-cache')
        result=self.run_script('--verify-only','--frp-archive',str(archive))
        self.assertNotEqual(result.returncode,0)
        self.assertIn('摘要不匹配'.encode(),result.stderr)
        self.assertFalse(self.marker.exists())

    def test_local_frp_symlink_rejected(self):
        linked=self.root/'linked-frp.tar.gz'
        linked.symlink_to(self.root/'frp_0.71.0_linux_amd64.tar.gz')
        result=self.run_script('--verify-only','--frp-archive',str(linked))
        self.assertNotEqual(result.returncode,0)
        self.assertIn('普通本地文件'.encode(),result.stderr)

    def test_no_terminal_fails_before_install(self):
        r = self.run_script()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('需要交互终端'.encode(), r.stderr)
        self.assertFalse(self.marker.exists())

    def test_curl_pipe_keeps_terminal_for_wizard(self):
        self.check_pipe('1')

    def test_curl_pipe_selects_adoption_wizard(self):
        self.env['EXPECT_COMMAND']='install adopt-wizard'
        self.check_pipe('2')

    def check_pipe(self, mode):
        pid, fd = pty.fork()
        if pid == 0:
            os.execvpe('bash', ['bash', '-c', 'cat "$BOOTSTRAP" | bash'], dict(self.env, BOOTSTRAP=str(SCRIPT)))
        transcript = b''
        selected=False
        try:
            deadline = time.monotonic()+15
            while time.monotonic() < deadline:
                if select.select([fd], [], [], .1)[0]:
                    try:
                        data = os.read(fd, 65536)
                    except OSError:
                        break
                    if not data:
                        break
                    transcript += data
                    if not selected and '请选择 [1]：'.encode() in transcript:
                        os.write(fd,(mode+'\n').encode())
                        selected=True
            done, status = os.waitpid(pid, os.WNOHANG)
            while not done and time.monotonic() < deadline:
                time.sleep(.01)
                done, status = os.waitpid(pid, os.WNOHANG)
            if not done:
                os.kill(pid, signal.SIGKILL)
                os.waitpid(pid, 0)
                self.fail('PTY bootstrap timed out')
            self.assertEqual(os.waitstatus_to_exitcode(status), 0, transcript.decode(errors='replace'))
            self.assertEqual(self.marker.read_text(), 'tty-ok')
        finally:
            os.close(fd)


if __name__ == '__main__':
    unittest.main()
