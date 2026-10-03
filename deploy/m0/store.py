"""Small persistent evidence index. No media deletion or production retention worker."""
import hashlib
from contextlib import contextmanager
import json
import math
from pathlib import Path
import re
import sqlite3
import time


def confined_path(root, path):
    root, path = Path(root).resolve(), Path(path).resolve()
    if not path.is_relative_to(root) or path == root:
        raise ValueError('媒体路径不属于录像根目录')
    return path


class Store:
    def __init__(self, database, storage):
        self.database = Path(database)
        self.database.parent.mkdir(parents=True, exist_ok=True)
        self.storage = Path(storage).resolve()
        with self.connection() as db:
            db.executescript('''
                PRAGMA journal_mode=WAL;
                CREATE TABLE IF NOT EXISTS recordings (
                  id TEXT PRIMARY KEY, channel TEXT, stream TEXT, path TEXT UNIQUE,
                  start REAL, end REAL, size INTEGER, timing TEXT, checked INTEGER DEFAULT 0,
                  evidence TEXT, created REAL);
                CREATE INDEX IF NOT EXISTS recordings_time ON recordings(channel,start,end);
                CREATE TABLE IF NOT EXISTS events (
                  id TEXT PRIMARY KEY, source_id TEXT, camera TEXT, channel TEXT, label TEXT,
                  start REAL, end REAL, state TEXT, frame REAL, zones TEXT, score REAL);
                CREATE INDEX IF NOT EXISTS events_time ON events(channel,start);
            ''')

    @contextmanager
    def connection(self):
        db = sqlite3.connect(self.database, timeout=15)
        db.row_factory = sqlite3.Row
        try:
            with db:
                yield db
        finally:
            db.close()

    def ingest_record(self, payload):
        stream = str(payload.get('stream', ''))
        match = re.fullmatch(r'ch(0[1-9]|[12][0-9]|3[0-2])_main_[a-f0-9]{8,64}', stream)
        if payload.get('app') != 'm0' or not match:
            raise ValueError('回调不属于 M0 主流')
        path = confined_path(self.storage, payload['file_path'])
        if path.suffix.lower() != '.mp4':
            raise ValueError('仅登记 MP4')
        start, duration = float(payload['start_time']), float(payload['time_len'])
        if not math.isfinite(start) or not math.isfinite(duration) or start < 0 or duration <= 0:
            raise ValueError('录像起止无效')
        relative = path.relative_to(self.storage).as_posix()
        identifier = hashlib.sha256(relative.encode()).hexdigest()[:32]
        with self.connection() as db:
            db.execute('''INSERT INTO recordings(id,channel,stream,path,start,end,size,timing,created)
                VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET
                start=excluded.start,end=excluded.end,size=excluded.size,timing=excluded.timing''',
                (identifier, 'CH' + match[1], stream, relative, start, start + duration,
                 int(payload.get('file_size', 0)), payload.get('timing', 'zlm-hook'), time.time()))
        return identifier

    def evidence(self, identifier, data):
        with self.connection() as db:
            db.execute('UPDATE recordings SET checked=1,evidence=? WHERE id=?', (json.dumps(data), identifier))

    def ingest_event(self, payload):
        event = payload.get('after') or payload.get('before') or {}
        camera = str(event.get('camera', ''))
        match = re.fullmatch(r'ch(0[1-9]|[12][0-9]|3[0-2])_[a-f0-9]{8,64}', camera)
        source_id = str(event.get('id', ''))
        if not match or not re.fullmatch(r'[A-Za-z0-9_.-]{1,120}', source_id):
            raise ValueError('事件来源无效')
        identifier = hashlib.sha256((camera + ':' + source_id).encode()).hexdigest()[:32]
        start = float(event['start_time'])
        end = float(event['end_time']) if event.get('end_time') is not None else None
        frame = float(event.get('frame_time') or end or start)
        if not all(math.isfinite(n) for n in [start, frame] + ([end] if end is not None else [])):
            raise ValueError('事件时间无效')
        kind = 'end' if end is not None else payload.get('type', 'update')
        if kind not in {'new', 'update', 'end'}:
            raise ValueError('事件类型无效')
        zones = event.get('entered_zones') or event.get('current_zones') or []
        zones = [str(zone)[:100] for zone in zones[:32]]
        with self.connection() as db:
            previous = db.execute('SELECT * FROM events WHERE id=?', (identifier,)).fetchone()
            if previous:
                # End is terminal; older or duplicate packets cannot overwrite newer evidence.
                if previous['state'] == 'end' and kind != 'end':
                    return identifier
                if frame < previous['frame']:
                    return identifier
                if previous['end'] is not None and end is None:
                    end, kind = previous['end'], 'end'
            db.execute('''INSERT INTO events VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id)
                DO UPDATE SET end=excluded.end,state=excluded.state,frame=excluded.frame,
                zones=excluded.zones,score=excluded.score''',
                (identifier, source_id, camera, 'CH' + match[1], str(event.get('label', 'unknown'))[:100],
                 start, end, kind, frame, json.dumps(zones), float(event.get('top_score') or event.get('score') or 0)))
        return identifier

    def recordings(self, channel=None, start=None, end=None, limit=200, stream=None):
        clauses, values = [], []
        if channel:
            clauses.append('channel=?'); values.append(channel)
        if stream:
            clauses.append('stream=?'); values.append(stream)
        if start is not None:
            clauses.append('end>?'); values.append(start)
        if end is not None:
            clauses.append('start<?'); values.append(end)
        where = ' WHERE ' + ' AND '.join(clauses) if clauses else ''
        with self.connection() as db:
            rows = db.execute('SELECT * FROM recordings' + where + ' ORDER BY start DESC LIMIT ?',
                              values + [min(int(limit), 1000)]).fetchall()
        return [dict(row) for row in rows]

    def record(self, identifier):
        with self.connection() as db:
            row = db.execute('SELECT * FROM recordings WHERE id=?', (identifier,)).fetchone()
        return dict(row) if row else None

    def events(self, identifier=None, limit=200):
        with self.connection() as db:
            if identifier:
                rows = db.execute('SELECT * FROM events WHERE id=?', (identifier,)).fetchall()
            else:
                rows = db.execute('SELECT * FROM events ORDER BY start DESC LIMIT ?', (min(int(limit), 1000),)).fetchall()
        result = [dict(row) for row in rows]
        for row in result:
            row['zones'] = json.loads(row['zones'])
        return result

    def unchecked(self):
        with self.connection() as db:
            return [dict(row) for row in db.execute('SELECT * FROM recordings WHERE checked=0 LIMIT 20')]
