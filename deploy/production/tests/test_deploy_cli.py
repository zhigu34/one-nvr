"""CLI contracts use disposable Docker stubs; Compose parsing uses the real CLI."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[3]
PIN = "registry.example/one-nvr:test@sha256:" + "a" * 64
MOCK = r'''#!/usr/bin/env python3
import json,os,sys,subprocess
from pathlib import Path
args=sys.argv[1:]; fixture=Path(os.environ['ONE_NVR_CLI_FIXTURE'])
with (fixture/'calls.jsonl').open('a') as f: f.write(json.dumps(args)+'\n')
if args[0]=='info': print('amd64')
elif args[:2]==['image','inspect']:
 state=json.loads((fixture/'built.json').read_text()) if (fixture/'built.json').exists() else {}
 if args[2]!=os.environ['ONE_NVR_CLI_ADMIN'] and args[2] not in state: sys.exit(1)
 if '--format' in args and 'source-tree' in args[-1]: print(state.get(args[2],''))
 else: print('amd64' if '--format' in args else '{}')
elif args[0]=='build':
 state=json.loads((fixture/'built.json').read_text()) if (fixture/'built.json').exists() else {}
 state[args[args.index('-t')+1]]=args[args.index('--label')+1].split('=',1)[1]
 (fixture/'built.json').write_text(json.dumps(state))
elif args[0]=='ps': pass
elif args[0]=='compose':
 if os.environ.get('ONE_NVR_REAL_COMPOSE') and 'run' in args:
  prefix=json.loads(os.environ['ONE_NVR_REAL_COMPOSE'])
  sys.exit(subprocess.run(prefix+args[1:args.index('run')]+['config','--quiet']).returncode)
 if 'setup-token' in args: print('fixture-initialization-token')
elif args[0]=='run':
 mounts={}
 for i,arg in enumerate(args):
  if arg=='--mount':
   fields=dict(x.split('=',1) for x in args[i+1].split(',') if '=' in x)
   mounts[fields['target']]=Path(fields['source'])
 entry=args[args.index('--entrypoint')+1]
 tail=args[args.index('--entrypoint')+3:]
 if entry=='cat': print((mounts['/output']/Path(tail[0]).name).read_text(),end='')
 elif entry=='test': sys.exit(0 if (mounts['/output']/Path(tail[-1]).name).is_file() else 1)
 elif entry=='sh': sys.stdin.read()
 elif entry=='python3': print('{}')
 elif 'env-value' in tail:
  values=dict(x.split('=',1) for x in mounts['/settings.env'].read_text().splitlines() if '=' in x)
  key={'data':'ONE_NVR_DATA_DIR','storage':'ONE_NVR_STORAGE_ROOT','tls':'ONE_NVR_TLS_DIR'}[tail[-1]]
  print(values.get(key,''))
 elif 'init-runtime' in tail: (mounts['/data']/'runtime').mkdir(exist_ok=True)
 elif 'render-deployment' in tail:
  p=mounts['/output']; p.mkdir(exist_ok=True)
  (p/'compose.json').write_text(json.dumps({'services':{'api':{'image':os.environ['ONE_NVR_CLI_ADMIN']}}}))
  (p/'services').write_text('api\nworker\npostgres\nzlm\ngateway\n')
  (p/'images').write_text(os.environ['ONE_NVR_CLI_ADMIN']+'\n')
 elif 'discover-hardware' in tail: print('{}')
 elif 'hardware-nodes' in tail: pass
else: raise RuntimeError(args)
'''


class DeployCLI(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="one-nvr-cli-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        tools = self.root / "tools"
        tools.mkdir()
        docker = tools / "docker"
        docker.write_text(MOCK)
        docker.chmod(0o755)
        self.env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                        ONE_NVR_CLI_FIXTURE=str(self.root), ONE_NVR_CLI_ADMIN=PIN)
        self.data = self.root / "data"
        self.data.mkdir()
        (self.root / "storage").mkdir()
        self.settings = self.root / "settings with spaces.env"
        self.settings.write_text(f"ONE_NVR_DATA_DIR={self.data}\nONE_NVR_STORAGE_ROOT={self.root / 'storage'}\n")

    def test_custom_admin_initialization_command_is_executable(self):
        run = subprocess.run([str(REPO / "deploy.sh"), "--env-file", str(self.settings),
                              "--admin-image", PIN, "--project", "one-nvr-cli-contract"],
                             env=self.env, cwd=self.root, capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        command = run.stdout.splitlines()[-1]
        token = subprocess.run(["bash", "-c", command], cwd=REPO, env=self.env,
                               capture_output=True, text=True)
        self.assertEqual(token.returncode, 0, token.stderr)
        self.assertIn("fixture-initialization-token", token.stdout)
        calls = [json.loads(x) for x in (self.root / "calls.jsonl").read_text().splitlines()]
        self.assertFalse(any(c[0] in {"build", "pull"} for c in calls), "prebuilt deployment rebuilt an image")
        last_compose = [c for c in calls if c[0] == "compose"][-1]
        self.assertIn("one-nvr-cli-contract", last_compose)
        self.assertIn("--env-file", last_compose)
        self.assertIn("/dev/null", last_compose)

    def build_default_admin(self, extra=""):
        with self.settings.open("a") as f:
            f.write(extra)
        return subprocess.run([str(REPO / "deploy.sh"), "--env-file", str(self.settings),
                               "--project", "one-nvr-build-contract"], env=self.env,
                              cwd=self.root, capture_output=True, text=True)

    def build_calls(self):
        return [c for c in (json.loads(x) for x in (self.root / "calls.jsonl").read_text().splitlines())
                if c[0] == "build"]

    def test_local_build_passes_reachable_default_proxy(self):
        run = self.build_default_admin()
        self.assertEqual(run.returncode, 0, run.stderr)
        builds = self.build_calls()
        self.assertTrue(builds)
        self.assertIn("GOPROXY=https://goproxy.cn,direct", builds[0])

    def test_local_build_reads_quoted_proxy_without_expansion(self):
        marker = self.root / "env-executed"
        run = self.build_default_admin('OTHER_BUILD_VALUE=$(touch ' + str(marker) + ')\n'
                                       '  ONE_NVR_GOPROXY = "https://proxy.golang.org,direct" # override\n')
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertIn("GOPROXY=https://proxy.golang.org,direct", self.build_calls()[0])
        self.assertFalse(marker.exists())

    def test_invalid_or_duplicate_proxy_fails_before_build(self):
        for extra in ["ONE_NVR_GOPROXY='$(touch sentinel)'\n",
                      "ONE_NVR_GOPROXY=https://goproxy.cn\nONE_NVR_GOPROXY=https://other.example\n"]:
            with self.subTest(extra=extra):
                self.settings.write_text(f"ONE_NVR_DATA_DIR={self.data}\nONE_NVR_STORAGE_ROOT={self.root / 'storage'}\n")
                log = self.root / "calls.jsonl"
                if log.exists():
                    log.unlink()
                run = self.build_default_admin(extra)
                self.assertNotEqual(run.returncode, 0)
                self.assertEqual(self.build_calls(), [])
                self.assertFalse((REPO / "sentinel").exists())

    def test_hardware_selection_ignores_ambient_project_dotenv(self):
        standalone = os.environ.get("ONE_NVR_TEST_COMPOSE_BIN")
        real = [standalone] if standalone else [shutil.which("docker"), "compose"]
        if not real[0]:
            self.skipTest("real Compose CLI required for config-only check")
        self.env["ONE_NVR_REAL_COMPOSE"] = json.dumps(real)
        # Put literal input beside the manifest to exercise ambient dotenv loading.
        (self.root / ".env").write_text("ONE_NVR_DATA_DIR=/srv/${ONE_NVR_UNSET_LITERAL:?literal-directory}\n")
        self.env.pop("ONE_NVR_UNSET_LITERAL", None)
        runtime = self.data / "runtime"
        runtime.mkdir()
        (self.data / "hardware").mkdir()
        (runtime / "image-frigate").write_text(PIN)
        manifest = self.root / "compose.json"
        manifest.write_text(json.dumps({"services": {"api": {"image": PIN}}}))
        helper = self.root / "deploy/production/probe-hardware.sh"
        helper.parent.mkdir(parents=True)
        # Hardware execution is a separate Linux-only contract. Exercise its
        # actual selection command here with the real Compose parser.
        selection = [line for line in (REPO / "deploy/production/probe-hardware.sh").read_text().splitlines()
                     if line.startswith("docker compose ")]
        self.assertEqual(len(selection), 1)
        helper.write_text('admin_image=$1; data=$2; compose_file=$3; project=$4\n' + selection[0] + '\n')
        run = subprocess.run(["bash", str(helper), PIN, str(self.data), str(manifest),
                              "one-nvr-hardware-contract"], cwd=self.root, env=self.env,
                             capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)


if __name__ == "__main__":
    unittest.main()
