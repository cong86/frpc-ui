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
VERSION = 'v0.1.0-preview.2'


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
        self.checksums()

    def tearDown(self):
        self.tmp.cleanup()

    def command(self, name, body):
        f = self.mock / name
        f.write_text('#!/usr/bin/env bash\nset -euo pipefail\n'+body+'\n')
        f.chmod(0o755)

    def archive(self, arch, member='frp-console'):
        body = b'#!/usr/bin/env bash\n[[ -t 0 ]] || exit 42\n[[ "$1 $2" == "install wizard" ]] || exit 43\nprintf "tty-ok" > "$MARKER"\n'
        with tarfile.open(self.root / f'frp-console_{VERSION}_linux_{arch}.tar.gz', 'w:gz') as tar:
            info = tarfile.TarInfo(member)
            info.size, info.mode = len(body), 0o755
            tar.addfile(info, io.BytesIO(body))

    def checksums(self):
        (self.root/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in sorted(self.root.glob('*.tar.gz'))))

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
        self.assertIn(b'Checksum mismatch', r.stderr)
        self.assertFalse(self.marker.exists())

    def test_unexpected_archive_member_rejected(self):
        self.archive('amd64', '../frp-console')
        self.checksums()
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(b'Unexpected archive members', r.stderr)

    def test_missing_checksum_rejected(self):
        (self.root/'SHA256SUMS').write_text('')
        r = self.run_script('--verify-only')
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(b'checksum missing', r.stderr)

    def test_unsupported_architecture_rejected(self):
        self.env['TEST_ARCH'] = 'mips'
        self.assertNotEqual(self.run_script('--verify-only').returncode, 0)

    def test_unsafe_version_rejected(self):
        self.assertNotEqual(self.run_script('--verify-only', '--version', '../../other').returncode, 0)

    def test_no_terminal_fails_before_install(self):
        r = self.run_script()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn(b'An interactive terminal is required', r.stderr)
        self.assertFalse(self.marker.exists())

    def test_curl_pipe_keeps_terminal_for_wizard(self):
        pid, fd = pty.fork()
        if pid == 0:
            os.execvpe('bash', ['bash', '-c', 'cat "$BOOTSTRAP" | bash'], dict(self.env, BOOTSTRAP=str(SCRIPT)))
        transcript = b''
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
