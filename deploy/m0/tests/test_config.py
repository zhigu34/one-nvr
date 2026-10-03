import sys
import subprocess
import json
import os
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from common import parse_channels, build_rtsp
from bootstrap import initialize

HEADER = 'channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n'


class ConfigTests(unittest.TestCase):
    def test_env_urls_keep_credentials_query_and_fixed_channel_numbers(self):
        from common import parse_env_channels
        url = 'rtsp://admin:p%40ss%3A%24word@camera.local:8554/main?x=1&y=%2F'
        rows = parse_env_channels({'camera2': url, 'camera2_sub': 'rtsp://camera.local/sub',
                                   'camera2_name': '走廊', 'camera1': 'rtsp://192.168.1.2/main'})
        self.assertEqual([row['channel_no'] for row in rows], ['CH01', 'CH02'])
        self.assertEqual(rows[1]['main_url'], url)
        self.assertEqual(rows[1]['sub_url'], 'rtsp://camera.local/sub')
        self.assertEqual(rows[1]['channel_name'], '走廊')
        self.assertIsNone(rows[0]['sub_url'])

    def test_env_source_generation_ignores_name_but_changes_with_url(self):
        from common import parse_env_channels
        first = parse_env_channels({'camera1': 'rtsp://[2001:db8::1]/main', 'camera1_name': '入口'})[0]
        renamed = parse_env_channels({'camera1': first['main_url'], 'camera1_name': '大门'})[0]
        replaced = parse_env_channels({'camera1': 'rtsp://[2001:db8::2]/main'})[0]
        self.assertEqual(first['generation'], renamed['generation'])
        self.assertNotEqual(first['generation'], replaced['generation'])
        self.assertEqual(first['channel_no'], replaced['channel_no'])

    def test_env_rejects_bad_urls_or_orphan_fields_without_leaking_credentials(self):
        from common import parse_env_channels
        invalid = [{}, {'camera33': 'rtsp://host/main'}, {'camera1_sub': 'rtsp://host/sub'},
                   {'camera1': 'http://admin:private-password@host/main'},
                   {'camera1': 'rtsp://admin:private-password@host:70000/main'},
                   {'camera1': 'rtsp://host/main#fragment'}, {'camera1': 'rtsp://host/a b'}]
        for env in invalid:
            with self.subTest(env=list(env)), self.assertRaises(ValueError) as result:
                parse_env_channels(env)
            self.assertNotIn('private-password', str(result.exception))

    def test_env_only_cli_generates_upstream_config_and_preserves_secrets(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = dict(os.environ, COMPOSE_PROFILES='intel', M0_MEDIA_HOST='192.168.1.20',
                       M0_RTC_PORT='8001', camera1='rtsp://admin:secret%24@camera.local/main',
                       camera1_sub='rtsp://camera.local/sub')
            command = [sys.executable, str(Path(__file__).resolve().parents[1] / 'bootstrap.py'),
                       '--directory', str(root), '--storage-root', str(root / 'media'), '--no-env', '--from-env']
            first = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(first.returncode, 0, first.stderr)
            before = (root / 'state/secrets/zlm_secret').read_bytes()
            second = subprocess.run(command, env=env, capture_output=True, text=True)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual(before, (root / 'state/secrets/zlm_secret').read_bytes())
            config = json.loads((root / 'state/frigate/config.yml').read_text())
            self.assertEqual(config['detectors']['ov']['device'], 'GPU')
            self.assertFalse(config['record']['enabled'])
            source = next(iter(config['cameras'].values()))['ffmpeg']['inputs'][0]['path']
            self.assertTrue(source.startswith('rtsp://zlm:554/m0/ch01_sub_'))
            settings = json.loads((root / 'state/probe/settings.json').read_text())
            self.assertEqual(settings['channels'][0]['main_url'], env['camera1'])
            self.assertIn('port = 8001', (root / 'state/zlm.ini').read_text())
            self.assertFalse((root / '.env').exists())

    def test_env_cli_rejects_multiple_hardware_profiles_before_writing_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = dict(os.environ, COMPOSE_PROFILES='cpu,intel', M0_MEDIA_HOST='192.168.1.20',
                       camera1='rtsp://admin:private-password@camera.local/main')
            result = subprocess.run([
                sys.executable, str(Path(__file__).resolve().parents[1] / 'bootstrap.py'),
                '--directory', str(root), '--no-env', '--from-env',
            ], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertIn('COMPOSE_PROFILES', result.stderr)
            self.assertNotIn('private-password', result.stderr)
            self.assertFalse((root / 'state').exists())

    def test_password_and_query_survive_url_generation(self):
        row = parse_channels(HEADER + 'CH01,入口,192.168.1.2,,admin,p@ss:word,/main?x=1&y=%2F,/sub\n')[0]
        self.assertEqual(build_rtsp(row, row['main_path']), 'rtsp://admin:p%40ss%3Aword@192.168.1.2:554/main?x=1&y=%2F')

    def test_ipv6_bom_and_empty_substream(self):
        row = parse_channels('\ufeff' + HEADER + 'CH02,走廊,2001:db8::1,8554,,,/main,\n')[0]
        self.assertEqual(build_rtsp(row, '/main'), 'rtsp://[2001:db8::1]:8554/main')
        self.assertEqual(row['sub_path'], '')

    def test_rejects_duplicates_ports_and_embedded_urls(self):
        good = 'CH01,入口,192.168.1.2,554,,,/main,/sub\n'
        for data in [good + good, good.replace(',554,', ',70000,'), good.replace('/main', 'rtsp://other/main'), good.replace('/main', '//other/main')]:
            with self.subTest(data=data), self.assertRaises(ValueError):
                parse_channels(HEADER + data)

    def test_csv_quotes_and_optional_onvif(self):
        row = parse_channels(HEADER.rstrip('\n') + ',onvif_port\nCH01,"入口,南",192.168.1.2,554,admin,"a,b",/main,/sub,8000\n')[0]
        self.assertEqual(row['password'], 'a,b')
        self.assertEqual(row['onvif_port'], 8000)

    def test_repeated_init_preserves_secrets_and_disables_frigate_recording(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            csv = root / 'cameras.csv'
            csv.write_text(HEADER + 'CH01,入口,192.168.1.2,554,admin,password,/main,/sub\n')
            initialize(root, csv, 'epyc-cpu', '192.168.1.10', generate_cert=False)
            before = (root / 'state/secrets/zlm_secret').read_bytes()
            initialize(root, csv, 'epyc-cpu', '192.168.1.10', generate_cert=False)
            self.assertEqual(before, (root / 'state/secrets/zlm_secret').read_bytes())
            import json
            config = json.loads((root / 'state/frigate/config.yml').read_text())
            self.assertFalse(config['record']['enabled'])
            self.assertEqual(config['detectors']['ov']['device'], 'CPU')
            source = next(iter(config['cameras'].values()))['ffmpeg']['inputs'][0]['path']
            self.assertIn('rtsp://zlm:554/m0/', source)
            self.assertNotIn('192.168.1.2', source)
            self.assertEqual((root / 'state/probe/settings.json').stat().st_mode & 0o777, 0o600)

    def test_gateway_alias_matches_the_observers_recording_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            csv = root / 'cameras.csv'
            csv.write_text(HEADER + 'CH01,入口,192.168.1.2,554,,,/main,\n')
            initialize(root, csv, 'epyc-cpu', '192.168.1.10', generate_cert=False)
            self.assertIn('alias /storage/m0-recordings/;', (root / 'state/nginx.conf').read_text())

    def test_container_init_keeps_host_env_and_writes_to_mounted_storage(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'workspace'
            root.mkdir()
            storage = Path(tmp) / 'mounted-storage'
            csv = root / 'cameras.csv'
            csv.write_text(HEADER + 'CH01,入口,192.168.1.2,554,,,/main,\n')
            initialize(root, csv, 'intel-igpu', '192.168.1.10', generate_cert=False,
                       storage_root=storage, write_env=False)
            self.assertFalse((root / '.env').exists())
            self.assertTrue((storage / 'm0-recordings').is_dir())
            self.assertTrue((root / 'state/operator.txt').is_file())
            env = root / '.env'
            env.write_text('M0_STORAGE_ROOT=/srv/recordings\n')
            initialize(root, csv, 'intel-igpu', '192.168.1.10', generate_cert=False,
                       storage_root=storage, write_env=False)
            self.assertEqual(env.read_text(), 'M0_STORAGE_ROOT=/srv/recordings\n')

    def test_docker_init_cli_generates_cert_without_container_paths_in_env(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'workspace'
            root.mkdir()
            (root / 'cameras.csv').write_text(HEADER + 'CH01,入口,192.168.1.2,554,,,/main,\n')
            result = subprocess.run([
                sys.executable, str(Path(__file__).resolve().parents[1] / 'bootstrap.py'),
                '--directory', str(root), '--storage-root', str(Path(tmp) / 'storage'),
                '--no-env', '--hardware', 'epyc-cpu', '--media-host', '192.168.1.10',
            ], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((root / 'state/tls/cert.pem').is_file())
            self.assertFalse((root / '.env').exists())


if __name__ == '__main__':
    unittest.main()
