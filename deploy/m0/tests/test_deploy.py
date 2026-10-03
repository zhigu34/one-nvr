import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class DeploymentTests(unittest.TestCase):
    def run_deployment(self, **options):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            script = root / 'deploy.sh'
            script.write_text((Path(__file__).resolve().parents[1] / 'deploy.sh').read_text())
            (root / '.env').write_text("camera1='$(touch should-not-exist)'\n")
            fake = root / 'docker'
            fake.write_text('''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
with open(os.environ['DOCKER_CALLS'], 'a') as file:
    file.write(json.dumps(args) + '\\n')
if args[:2] == ['image', 'inspect']:
    if os.environ.get('MISSING_BASE') == '1': sys.exit(1)
    print(os.environ.get('IMAGE_ARCH', 'amd64'))
elif args[:2] == ['context', 'show']: print('default')
elif args[0] == 'info': print('x86_64')
elif args[:2] == ['buildx', 'inspect']:
    print('Name: default\\nDriver: ' + os.environ.get('BUILDER_DRIVER', 'docker'))
elif args[:3] == ['compose', 'build', '--help']: print('--builder string')
elif args[:2] == ['compose', 'build']:
    if os.environ.get('BUILD_FAIL') == '1': sys.exit(42)
''')
            fake.chmod(0o755)
            calls = root / 'calls.jsonl'
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'],
                       DOCKER_CALLS=str(calls), **options)
            result = subprocess.run(['bash', str(script)], env=env, capture_output=True, text=True)
            commands = [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else []
            self.assertFalse((root / 'should-not-exist').exists(), '.env must never be sourced as shell code')
            return result, commands

    def test_builds_shared_image_once_with_engine_builder_then_starts_without_pulls(self):
        result, commands = self.run_deployment()
        self.assertEqual(result.returncode, 0, result.stderr)
        builds = [call for call in commands if call[:2] == ['compose', 'build'] and '--help' not in call]
        self.assertEqual(builds, [['compose', 'build', '--builder', 'default', 'init']])
        self.assertIn(['compose', 'up', '-d', '--no-build', '--pull', 'never'], commands)
        self.assertFalse(any('pull' == call[0] for call in commands))

    def test_missing_local_python_fails_before_build_or_start(self):
        result, commands = self.run_deployment(MISSING_BASE='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('python:3.12-slim', result.stderr)
        self.assertFalse(any(call[:2] in [['compose', 'build'], ['compose', 'up']] for call in commands))

    def test_wrong_architecture_or_isolated_builder_fails_before_build(self):
        for options in [{'IMAGE_ARCH': 'arm64'}, {'BUILDER_DRIVER': 'docker-container'}]:
            with self.subTest(options=options):
                result, commands = self.run_deployment(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(any(call[:2] in [['compose', 'build'], ['compose', 'up']] for call in commands))

    def test_failed_build_does_not_start_services(self):
        result, commands = self.run_deployment(BUILD_FAIL='1')
        self.assertEqual(result.returncode, 42)
        self.assertFalse(any(call[:2] == ['compose', 'up'] for call in commands))


if __name__ == '__main__':
    unittest.main()
