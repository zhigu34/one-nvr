"""M0 observer: private hooks + MQTT + minimal read-only browser APIs."""
import concurrent.futures
from datetime import datetime, timezone
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import threading
import time
from urllib.parse import parse_qs, quote, urlencode, urlsplit
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from store import Store, confined_path


def fetch(url, body=None, content_type='application/json', timeout=10):
    request = Request(url, data=body, headers={'Content-Type': content_type} if body is not None else {})
    with urlopen(request, timeout=timeout) as response:
        return response.read(4 * 1024 * 1024)


class Probe:
    def __init__(self, settings_path, database, storage):
        self.settings = json.loads(Path(settings_path).read_text())
        self.store = Store(database, storage)
        self.storage = Path(storage)
        self.stop = threading.Event()
        self.status_lock = threading.Lock()
        self.status = {'mqtt': False, 'zlm': False, 'frigate': False, 'streams': [], 'last_reconcile': None,
                       'hardware_profile': self.settings['hardware'], 'errors': []}
        self.snapshots = Path(database).parent / 'snapshots'
        self.snapshots.mkdir(exist_ok=True)
        self.channels = {channel['channel_no']: channel for channel in self.settings['channels']}
        self.mqtt = None

    def zlm(self, method, **params):
        payload = urlencode({'secret': self.settings['zlm_secret'], **params}).encode()
        data = json.loads(fetch('http://zlm:80/index/api/' + method, payload, 'application/x-www-form-urlencoded'))
        if data.get('code') != 0:
            raise RuntimeError('ZLM ' + method + ' code=' + str(data.get('code')))
        return data

    def reconcile(self):
        while not self.stop.is_set():
            errors, observed = [], []
            try:
                actual = self.zlm('getMediaList', schema='rtsp', app='m0').get('data') or []
                active = {item['stream']: item for item in actual}
                for channel in self.channels.values():
                    for role in ['main', 'sub']:
                        url, stream = channel.get(role + '_url'), channel[role + '_stream']
                        if not url:
                            continue
                        try:
                            if stream not in active:
                                self.zlm('addStreamProxy', vhost='__defaultVhost__', app='m0', stream=stream,
                                         url=url, rtp_type=0, retry_count=-1, enable_mp4=int(role == 'main'),
                                         mp4_save_path='/storage/m0-recordings', mp4_max_second=60,
                                         enable_rtsp=1, enable_rtmp=0, enable_hls=0, enable_ts=0,
                                         enable_fmp4=0, auto_close=0)
                            elif role == 'main':
                                state = self.zlm('isRecording', type=1, vhost='__defaultVhost__', app='m0', stream=stream)
                                if not state.get('status'):
                                    self.zlm('startRecord', type=1, vhost='__defaultVhost__', app='m0', stream=stream,
                                             customized_path='/storage/m0-recordings', max_second=60)
                            item = active.get(stream, {})
                            tracks = [{key: track.get(key) for key in ['codec_id_name', 'codec_type', 'fps', 'width', 'height']}
                                      for track in item.get('tracks', [])]
                            observed.append({'channel': channel['channel_no'], 'role': role, 'stream': stream,
                                             'listed': stream in active, 'bytes_speed': item.get('bytesSpeed'), 'tracks': tracks})
                        except Exception as error:
                            errors.append(channel['channel_no'] + '/' + role + ': ' + type(error).__name__)
                zlm_ok = True
            except Exception as error:
                zlm_ok = False
                errors.append('ZLM: ' + type(error).__name__)
            try:
                stats = json.loads(fetch('http://frigate:5000/api/stats'))
                frigate_ok = True
                detector_stats = {name: {'inference_speed': value.get('inference_speed'), 'pid': value.get('pid')}
                                  for name, value in stats.get('detectors', {}).items()}
                cameras = {name: {key: value.get(key) for key in ['camera_fps', 'process_fps', 'skipped_fps', 'detection_fps']}
                           for name, value in stats.get('cameras', {}).items()}
            except Exception as error:
                frigate_ok, detector_stats, cameras = False, {}, {}
                errors.append('Frigate: ' + type(error).__name__)
            with self.status_lock:
                self.status.update(zlm=zlm_ok, frigate=frigate_ok, streams=observed, errors=errors,
                                   last_reconcile=time.time(), detectors=detector_stats, cameras=cameras)
            self.stop.wait(15)

    def start_mqtt(self):
        import paho.mqtt.client as mqtt
        client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id='one-nvr-m0-probe')
        client.username_pw_set('m0', self.settings['mqtt_password'])
        client.reconnect_delay_set(1, 30)

        def connected(client, userdata, flags, reason, properties):
            with self.status_lock:
                self.status['mqtt'] = not reason.is_failure
            if not reason.is_failure:
                client.subscribe('frigate/events', qos=1)

        def disconnected(client, userdata, flags, reason, properties):
            with self.status_lock:
                self.status['mqtt'] = False

        def message(client, userdata, packet):
            if len(packet.payload) > 1048576:
                return
            try:
                self.store.ingest_event(json.loads(packet.payload))
            except (ValueError, KeyError, TypeError):
                print('MQTT event rejected: invalid payload', flush=True)

        client.on_connect, client.on_disconnect, client.on_message = connected, disconnected, message
        client.connect_async('mqtt', 1883, 60)
        client.loop_start()
        self.mqtt = client

    def probe_file(self, path):
        path = confined_path(self.storage, path)
        result = subprocess.run(['ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json', str(path)],
                                capture_output=True, timeout=20)
        if result.returncode:
            return {'readable': False, 'level': 'ffprobe-structure', 'error': 'ffprobe failed'}
        info = json.loads(result.stdout)
        video = [stream for stream in info.get('streams', []) if stream.get('codec_type') == 'video']
        return {'readable': bool(video), 'level': 'ffprobe-structure', 'duration': info.get('format', {}).get('duration'),
                'video': [{key: stream.get(key) for key in ['codec_name', 'width', 'height', 'avg_frame_rate']} for stream in video]}

    def scan(self):
        while not self.stop.is_set():
            try:
                # Complete callbacks are verified first. FFprobe metadata is not full-frame completeness proof.
                for row in self.store.unchecked():
                    path = confined_path(self.storage, self.storage / row['path'])
                    if path.is_file():
                        self.store.evidence(row['id'], self.probe_file(path))
                # Recover missed callbacks from aged files. Filename time is provisional, visibly tagged.
                for path in self.storage.rglob('*.mp4'):
                    if not path.is_file() or time.time() - path.stat().st_mtime < 120:
                        continue
                    stream = next((part for part in path.parts if re.fullmatch(r'ch\d{2}_main_[a-f0-9]{8,64}', part)), None)
                    if not stream:
                        continue
                    relative = path.resolve().relative_to(self.storage.resolve()).as_posix()
                    import hashlib
                    identifier = hashlib.sha256(relative.encode()).hexdigest()[:32]
                    if self.store.record(identifier):
                        continue
                    evidence = self.probe_file(path)
                    if not evidence.get('readable'):
                        continue
                    date = next((part for part in path.parts if re.fullmatch(r'\d{4}-\d{2}-\d{2}', part)), None)
                    full = re.match(r'(\d{4}-\d{2}-\d{2})-(\d{2})-(\d{2})-(\d{2})(?:-|$)', path.stem)
                    hour = re.match(r'(\d{2})-(\d{2})-(\d{2})(?:-|$)', path.stem)
                    if full:
                        timestamp = full[1] + ' ' + ':'.join(full.groups()[1:])
                    elif date and hour:
                        timestamp = date + ' ' + ':'.join(hour.groups())
                    else:
                        continue
                    moment = datetime.strptime(timestamp, '%Y-%m-%d %H:%M:%S').replace(tzinfo=timezone.utc)
                    identifier = self.store.ingest_record({'app': 'm0', 'stream': stream, 'file_path': str(path),
                        'start_time': moment.timestamp(), 'time_len': float(evidence['duration']),
                        'file_size': path.stat().st_size, 'timing': 'filename-provisional'})
                    self.store.evidence(identifier, evidence)
            except Exception as error:
                print('Media scan error: ' + type(error).__name__, flush=True)
            self.stop.wait(60)


def make_handler(probe):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def respond(self, payload, status=200, content_type='application/json'):
            data = json.dumps(payload, ensure_ascii=False).encode() if content_type == 'application/json' else payload
            self.send_response(status)
            self.send_header('Content-Type', content_type)
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Cache-Control', 'no-store')
            self.end_headers()
            self.wfile.write(data)

        def do_GET(self):
            parsed = urlsplit(self.path)
            query = parse_qs(parsed.query)
            try:
                if parsed.path == '/health':
                    return self.respond({'alive': True})
                if parsed.path == '/api/status':
                    with probe.status_lock:
                        status = dict(probe.status)
                    status['channels'] = [{key: channel[key] for key in ['channel_no', 'channel_name', 'camera_key']}
                                          for channel in probe.channels.values()]
                    return self.respond(status)
                if parsed.path == '/api/recordings':
                    return self.respond(probe.store.recordings(channel=query.get('channel', [None])[0],
                        start=float(query['start'][0]) if query.get('start') else None,
                        end=float(query['end'][0]) if query.get('end') else None))
                if parsed.path == '/api/events':
                    rows = probe.store.events()
                    for row in rows:
                        row['snapshot_cached'] = (probe.snapshots / (row['id'] + '.jpg')).is_file()
                    return self.respond(rows)
                match = re.fullmatch(r'/api/events/([a-f0-9]{32})/recordings', parsed.path)
                if match:
                    rows = probe.store.events(match[1])
                    if not rows:
                        return self.respond({'error': '事件不存在'}, 404)
                    event = rows[0]
                    camera_channel, generation = event['camera'].split('_', 1)
                    return self.respond(probe.store.recordings(channel=event['channel'], start=event['start'] - 10,
                        end=(event['end'] or event['frame']) + 20, limit=1000,
                        stream=camera_channel + '_main_' + generation))
                match = re.fullmatch(r'/api/events/([a-f0-9]{32})/snapshot', parsed.path)
                if match:
                    rows = probe.store.events(match[1])
                    if not rows:
                        return self.respond({'error': '事件不存在'}, 404)
                    path = probe.snapshots / (match[1] + '.jpg')
                    if not path.exists():
                        event = rows[0]
                        if event['state'] == 'end' and event['has_snapshot'] is False:
                            return self.respond({'code': 'snapshot_not_saved',
                                'error': 'Frigate 未为此事件保存抓拍；仍可查看关联录像。'}, 404)
                        try:
                            raw = fetch('http://frigate:5000/api/events/' + quote(event['source_id'], safe='') + '/snapshot.jpg')
                        except HTTPError as error:
                            error.close()
                            if error.code != 404:
                                raise
                            return self.respond({'code': 'snapshot_not_found',
                                'error': 'Frigate 当前没有此抓拍（尚未生成、未保存或已清理）；不影响本地事件和录像索引。'}, 404)
                        if not raw.startswith(b'\xff\xd8'):
                            return self.respond({'error': '抓拍尚未就绪'}, 404)
                        path.write_bytes(raw)
                    return self.respond(path.read_bytes(), content_type='image/jpeg')
                match = re.fullmatch(r'/media/([a-f0-9]{32})', parsed.path)
                if match:
                    row = probe.store.record(match[1])
                    if not row:
                        return self.respond({'error': '录像不存在'}, 404)
                    path = confined_path(probe.storage, probe.storage / row['path'])
                    if not path.is_file() or not row['checked'] or not json.loads(row['evidence']).get('readable'):
                        return self.respond({'error': '录像未验证、缺失或损坏'}, 409)
                    self.send_response(200)
                    self.send_header('Content-Type', 'video/mp4')
                    self.send_header('X-Accel-Redirect', '/internal-media/' + quote(row['path'], safe='/'))
                    self.end_headers()
                    return
                self.respond({'error': '不存在'}, 404)
            except (ValueError, KeyError, TypeError):
                self.respond({'error': '请求参数无效'}, 400)
            except Exception:
                self.respond({'error': '上游或媒体读取失败'}, 502)

        def do_POST(self):
            path = urlsplit(self.path).path
            try:
                size = int(self.headers.get('Content-Length', '0'))
                if not 0 < size <= 1048576:
                    return self.respond({'error': '请求长度无效'}, 413)
                payload = json.loads(self.rfile.read(size))
                if path.startswith('/hooks/'):
                    parts = path.split('/')
                    if len(parts) != 4 or not hmac.compare_digest(parts[2], probe.settings['hook_token']):
                        return self.respond({'code': -1}, 403)
                    if parts[3] == 'record':
                        probe.store.ingest_record(payload)
                        return self.respond({'code': 0})
                    if parts[3] == 'none-reader':
                        return self.respond({'code': 0, 'close': False})
                if path == '/api/webrtc':
                    channel = probe.channels[payload['channel']]
                    role = 'sub' if payload.get('sub') and channel['sub_url'] else 'main'
                    parameters = urlencode({'app': 'm0', 'stream': channel[role + '_stream'],
                                             'type': 'play', 'secret': probe.settings['zlm_secret']})
                    raw = fetch('http://zlm:80/index/api/webrtc?' + parameters,
                                payload['offer'].encode(), 'application/sdp')
                    return self.respond(json.loads(raw))
                self.respond({'error': '不存在'}, 404)
            except (ValueError, KeyError, TypeError):
                self.respond({'error': '请求无效'}, 400)
            except Exception:
                self.respond({'error': '上游请求失败'}, 502)

    return Handler


def main():
    probe = Probe('/state/settings.json', '/state/index.sqlite', '/storage/m0-recordings')
    probe.start_mqtt()
    for worker in [probe.reconcile, probe.scan]:
        threading.Thread(target=worker, daemon=True).start()
    server = ThreadingHTTPServer(('0.0.0.0', 8080), make_handler(probe))
    server.serve_forever()


if __name__ == '__main__':
    main()
