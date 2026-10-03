import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from store import Store, confined_path


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.store = Store(self.root / 'index.sqlite', self.root / 'storage')
        self.path = self.root / 'storage/record/m0/ch01_main_abcdef12/2026-10-03/1.mp4'
        self.path.parent.mkdir(parents=True)
        self.path.write_bytes(b'example')

    def tearDown(self):
        self.tmp.cleanup()

    def test_confines_paths_and_symlinks(self):
        with self.assertRaises(ValueError):
            confined_path(self.root / 'storage', self.root / 'outside.mp4')
        (self.root / 'outside.mp4').write_text('outside')
        link = self.root / 'storage/link.mp4'
        link.symlink_to(self.root / 'outside.mp4')
        with self.assertRaises(ValueError):
            confined_path(self.root / 'storage', link)

    def test_record_hook_idempotency_and_overlap(self):
        payload = {'app': 'm0', 'stream': 'ch01_main_abcdef12', 'file_path': str(self.path), 'start_time': 1000, 'time_len': 60, 'file_size': 7}
        self.store.ingest_record(payload)
        self.store.ingest_record(payload)
        self.assertEqual(len(self.store.recordings()), 1)
        self.assertEqual(len(self.store.recordings(channel='CH01', start=1059, end=1061)), 1)
        self.assertEqual(len(self.store.recordings(channel='CH02')), 0)
        self.assertEqual(len(self.store.recordings(channel='CH01', start=1060, end=1070)), 0)

    def test_late_update_does_not_reopen_ended_event(self):
        event = {'id': '1000-abcd', 'camera': 'ch01_abcdef12', 'label': 'person', 'start_time': 1000, 'frame_time': 1010, 'end_time': None, 'entered_zones': ['entry']}
        self.store.ingest_event({'type': 'new', 'after': event})
        self.store.ingest_event({'type': 'end', 'after': {**event, 'frame_time': 1020, 'end_time': 1020}})
        self.store.ingest_event({'type': 'update', 'after': {**event, 'frame_time': 1015}})
        row = self.store.events()[0]
        self.assertEqual(row['end'], 1020)
        self.assertEqual(row['state'], 'end')
        self.assertEqual(len(self.store.events()), 1)

    def test_rejects_unmapped_stream(self):
        with self.assertRaises(ValueError):
            self.store.ingest_record({'app': 'other', 'stream': 'other', 'file_path': str(self.path), 'start_time': 1, 'time_len': 60})

    def test_database_connection_is_closed_after_transaction(self):
        import sqlite3
        with self.store.connection() as db:
            db.execute('SELECT 1')
        with self.assertRaises(sqlite3.ProgrammingError):
            db.execute('SELECT 1')


if __name__ == '__main__':
    unittest.main()
