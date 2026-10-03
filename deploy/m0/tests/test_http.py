import json
from pathlib import Path
import sys
import tempfile
import os
import threading
import unittest
from unittest.mock import patch
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from bootstrap import initialize
from probe import Probe, make_handler

HEADER = 'channel_no,channel_name,ip,rtsp_port,username,password,main_path,sub_path\n'


class HttpTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        csv = self.root / 'cameras.csv'
        csv.write_text(HEADER + 'CH01,入口,192.168.1.2,554,admin,private-camera-password,/main,/sub\n')
        initialize(self.root, csv, 'epyc-cpu', '192.168.1.10', generate_cert=False)
        self.probe = Probe(self.root / 'state/probe/settings.json', self.root / 'state/probe/index.sqlite',
                           self.root / 'storage/m0-recordings')
        self.server = ThreadingHTTPServer(('127.0.0.1', 0), make_handler(self.probe))
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.url = 'http://127.0.0.1:' + str(self.server.server_port)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.worker.join()
        self.tmp.cleanup()

    def test_status_does_not_expose_credentials(self):
        raw = urlopen(self.url + '/api/status', timeout=3).read()
        self.assertNotIn(b'private-camera-password', raw)
        self.assertNotIn(self.probe.settings['zlm_secret'].encode(), raw)
        self.assertNotIn(b'192.168.1.2', raw)
        self.assertEqual(json.loads(raw)['channels'][0]['channel_no'], 'CH01')

    def test_private_hook_requires_token_and_none_reader_keeps_recording(self):
        with self.assertRaises(HTTPError) as result:
            urlopen(Request(self.url + '/hooks/incorrect/none-reader', data=b'{}'), timeout=3)
        self.assertEqual(result.exception.code, 403)
        token = self.probe.settings['hook_token']
        response = urlopen(Request(self.url + '/hooks/' + token + '/none-reader', data=b'{}'), timeout=3)
        self.assertEqual(json.loads(response.read()), {'code': 0, 'close': False})

    def test_completed_file_requires_evidence_then_redirects_to_confined_media(self):
        path = self.root / 'storage/m0-recordings/record/test.mp4'
        path.parent.mkdir(parents=True)
        path.write_bytes(b'mp4-placeholder')
        channel = next(iter(self.probe.channels.values()))
        record = self.probe.store.ingest_record({'app': 'm0', 'stream': channel['main_stream'],
            'file_path': str(path), 'start_time': 1000, 'time_len': 60, 'file_size': 15})
        with self.assertRaises(HTTPError) as result:
            urlopen(self.url + '/media/' + record, timeout=3)
        self.assertEqual(result.exception.code, 409)
        self.probe.store.evidence(record, {'readable': True, 'level': 'test-evidence'})
        response = urlopen(self.url + '/media/' + record, timeout=3)
        self.assertEqual(response.headers['X-Accel-Redirect'], '/internal-media/record/test.mp4')

    def test_scanner_recovers_zlm_full_timestamp_filename(self):
        from datetime import datetime, timezone
        class OneIteration:
            done = False
            def is_set(self): return self.done
            def wait(self, seconds): self.done = True
        path = self.probe.storage / 'record/m0/ch01_main_abcdef12/2026-10-03/2026-10-03-12-34-56-0.mp4'
        path.parent.mkdir(parents=True)
        path.write_bytes(b'test')
        os.utime(path, (1, 1))
        self.probe.stop = OneIteration()
        with patch.object(self.probe, 'probe_file', return_value={'readable': True, 'duration': '60'}):
            self.probe.scan()
        rows = self.probe.store.recordings()
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]['start'], datetime(2026, 10, 3, 12, 34, 56, tzinfo=timezone.utc).timestamp())
        self.assertEqual(rows[0]['timing'], 'filename-provisional')

    def test_event_window_excludes_replacement_source_generation(self):
        old_id = None
        for generation in ['abcdef12', 'abcdef34']:
            path = self.probe.storage / (generation + '.mp4')
            path.write_bytes(b'test')
            identifier = self.probe.store.ingest_record({'app': 'm0', 'stream': 'ch01_main_' + generation,
                'file_path': str(path), 'start_time': 1000, 'time_len': 60, 'file_size': 4})
            if generation == 'abcdef12': old_id = identifier
        event_id = self.probe.store.ingest_event({'type': 'end', 'after': {'id': '1000-abc',
            'camera': 'ch01_abcdef12', 'label': 'person', 'start_time': 1020, 'end_time': 1030}})
        rows = json.loads(urlopen(self.url + '/api/events/' + event_id + '/recordings', timeout=3).read())
        self.assertEqual([row['id'] for row in rows], [old_id])


if __name__ == '__main__':
    unittest.main()
