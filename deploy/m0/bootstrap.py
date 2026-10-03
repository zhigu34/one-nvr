#!/usr/bin/env python3
"""Generate local M0 configuration. Does not start Docker or touch a running NVR."""
import argparse
import base64
import configparser
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import subprocess

from common import parse_channels, parse_env_channels, build_rtsp


def private_write(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding='utf-8')
    path.chmod(0o600)


def get_secret(directory, name):
    path = directory / name
    if not path.exists():
        private_write(path, secrets.token_hex(24) + '\n')
    return path.read_text().strip()


def initialize(directory, csv_file, hardware, media_host, generate_cert=True, storage_root=None, write_env=True,
               channels=None, rtc_port=8000):
    directory = Path(directory).resolve()
    if hardware not in {'epyc-cpu', 'intel-igpu'}:
        raise ValueError('硬件档案仅支持 epyc-cpu 或 intel-igpu')
    # M0 LAN deployment: a literal host IP avoids ambiguous ICE/DNS behavior.
    ipaddress.ip_address(media_host)
    if not 1 <= rtc_port <= 65535:
        raise ValueError('M0_RTC_PORT 需为 1–65535')
    if channels is None:
        channels = parse_channels(Path(csv_file).read_text(encoding='utf-8-sig'))
        for channel in channels:
            channel['main_url'] = build_rtsp(channel, channel['main_path'])
            channel['sub_url'] = build_rtsp(channel, channel['sub_path']) if channel['sub_path'] else None
    state = directory / 'state'
    storage = Path(storage_root).resolve() if storage_root else directory / 'storage'
    if any(c in str(storage) + str(state) for c in '\n\r$'):
        raise ValueError('部署目录不能包含换行或 $')
    for path in [state / 'secrets', state / 'probe', state / 'frigate', state / 'frigate-media',
                 state / 'mqtt-data', state / 'mqtt-auth', state / 'tls', storage / 'm0-recordings']:
        path.mkdir(parents=True, exist_ok=True)
    secret_dir = state / 'secrets'
    zlm_secret = get_secret(secret_dir, 'zlm_secret')
    hook_token = get_secret(secret_dir, 'hook_token')
    mqtt_password = get_secret(secret_dir, 'mqtt_password')
    web_password = get_secret(secret_dir, 'web_password')
    private_write(state / 'operator.txt', '用户名：admin\n密码：' + web_password + '\n')
    hashed = base64.b64encode(hashlib.sha1(web_password.encode()).digest()).decode()
    private_write(state / 'htpasswd', 'admin:{SHA}' + hashed + '\n')

    # Serve a generated public copy rather than a host checkout whose umask or
    # archive permissions may make the bind-mounted page unreadable to Nginx.
    homepage = state / 'web/index.html'
    private_write(homepage, (Path(__file__).resolve().parent / 'static/index.html').read_text(encoding='utf-8'))
    homepage.parent.chmod(0o755)
    homepage.chmod(0o644)

    conf = configparser.ConfigParser(interpolation=None)
    conf.optionxform = str
    conf.read_dict({
        'api': {'secret': zlm_secret},
        'general': {'mediaServerId': 'one-nvr-m0', 'enableVhost': '0', 'streamNoneReaderDelayMS': '15000'},
        'protocol': {'enable_mp4': '0', 'mp4_max_second': '60', 'mp4_save_path': '/storage/m0-recordings',
                     'enable_hls': '0', 'enable_rtmp': '0', 'enable_ts': '0', 'enable_fmp4': '0',
                     'enable_rtsp': '1', 'auto_close': '0', 'modify_stamp': '2', 'add_mute_audio': '0'},
        'record': {'fastStart': '1', 'fileRepeat': '0'},
        'rtsp': {'port': '554', 'sslport': '0', 'directProxy': '0'},
        'http': {'port': '80', 'sslport': '0', 'dirMenu': '0'},
        'rtc': {'externIP': media_host, 'port': str(rtc_port), 'tcpPort': str(rtc_port)},
        'hook': {'enable': '1', 'timeoutSec': '5', 'retry': '3',
                 'on_record_mp4': f'http://probe:8080/hooks/{hook_token}/record',
                 'on_stream_none_reader': f'http://probe:8080/hooks/{hook_token}/none-reader'},
        'shell': {'port': '0'},
    })
    import io
    text = io.StringIO()
    conf.write(text)
    private_write(state / 'zlm.ini', text.getvalue())

    frigate = {
        'mqtt': {'enabled': True, 'host': 'mqtt', 'port': 1883, 'user': 'm0', 'password': mqtt_password,
                 'topic_prefix': 'frigate', 'client_id': 'one-nvr-m0-frigate'},
        'detectors': {'ov': {'type': 'openvino', 'device': 'GPU' if hardware == 'intel-igpu' else 'CPU'}},
        'model': {'path': '/openvino-model/ssdlite_mobilenet_v2.xml', 'labelmap_path': '/openvino-model/coco_91cl_bkgr.txt',
                  'width': 300, 'height': 300, 'input_tensor': 'nhwc', 'input_pixel_format': 'bgr', 'model_type': 'ssd'},
        'record': {'enabled': False},
        'snapshots': {'enabled': True, 'timestamp': True, 'bounding_box': True, 'retain': {'default': 7}},
        'objects': {'track': ['person', 'car']},
        'birdseye': {'enabled': False},
        'tls': {'enabled': False},
        'cameras': {},
    }
    if hardware == 'intel-igpu':
        frigate['ffmpeg'] = {'hwaccel_args': 'preset-vaapi'}
    for channel in channels:
        source = channel['sub_stream'] if channel['sub_url'] else channel['main_stream']
        frigate['cameras'][channel['camera_key']] = {
            'ffmpeg': {'inputs': [{'path': f'rtsp://zlm:554/m0/{source}', 'input_args': 'preset-rtsp-generic', 'roles': ['detect']}]},
            'detect': {'enabled': True, 'width': 640, 'height': 360, 'fps': 5},
            'zones': {'entry': {'coordinates': '0,0,1,0,1,1,0,1', 'inertia': 3, 'objects': ['person', 'car']}},
        }
    # JSON is valid YAML; camera strings and special credentials remain exact literals.
    private_write(state / 'frigate/config.yml', json.dumps(frigate, ensure_ascii=False, indent=2) + '\n')
    settings = {'channels': channels, 'hardware': hardware, 'zlm_secret': zlm_secret,
                'hook_token': hook_token, 'mqtt_password': mqtt_password, 'record_seconds': 60}
    private_write(state / 'probe/settings.json', json.dumps(settings, ensure_ascii=False, indent=2))
    private_write(state / 'mosquitto.conf', 'listener 1883\nallow_anonymous false\npassword_file /mosquitto/auth/passwords\npersistence true\npersistence_location /mosquitto/data/\nlog_dest stdout\n')
    private_write(state / 'nginx.conf', '''events {}
http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log off;
    server {
        listen 443 ssl;
        ssl_certificate /m0/tls/cert.pem;
        ssl_certificate_key /m0/tls/key.pem;
        auth_basic "one-nvr M0";
        auth_basic_user_file /m0/htpasswd;
        client_max_body_size 1m;
        location /api/ {
            proxy_pass http://probe:8080;
            proxy_set_header Authorization "";
            add_header Cache-Control "no-store" always;
        }
        location /media/ {
            proxy_pass http://probe:8080;
            proxy_set_header Authorization "";
        }
        location /internal-media/ {
            internal;
            alias /storage/m0-recordings/;
            add_header Cache-Control "no-store" always;
        }
        location / { root /usr/share/nginx/html; try_files $uri $uri/ =404; }
    }
}
''')
    # Nginx and Mosquitto unprivileged processes need read access to non-secret config.
    (state / 'mosquitto.conf').chmod(0o644)
    (state / 'htpasswd').chmod(0o644)
    if generate_cert and not (state / 'tls/key.pem').exists():
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '365',
                        '-keyout', str(state / 'tls/key.pem'), '-out', str(state / 'tls/cert.pem'),
                        '-subj', '/CN=one-nvr-m0', '-addext', 'subjectAltName=IP:' + media_host],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (state / 'tls/key.pem').chmod(0o600)
    env = directory / '.env'
    if write_env and not env.exists():
        private_write(env, f'M0_STATE_DIR={state}\nM0_STORAGE_ROOT={storage}\nM0_HTTPS_PORT=8443\nM0_GPU_DEVICE=/dev/dri/renderD128\n')
    return channels


def main():
    parser = argparse.ArgumentParser(description='生成 M0 配置；修改摄像头前先停止 Compose，避免覆盖运行配置')
    parser.add_argument('--cameras', default='cameras.csv')
    parser.add_argument('--from-env', action='store_true', help='读取 camera1…camera32 完整 URL，不读取 CSV')
    parser.add_argument('--directory', default=str(Path(__file__).resolve().parent),
                        help='配置输出目录；容器初始化使用挂载的 /workspace')
    parser.add_argument('--no-env', action='store_true', help='不生成 .env，保留宿主 Compose 挂载配置')
    parser.add_argument('--hardware', choices=['epyc-cpu', 'intel-igpu'])
    parser.add_argument('--media-host', default=os.environ.get('M0_MEDIA_HOST'), help='浏览器可达的服务器局域网 IP')
    parser.add_argument('--storage-root', help='宿主录像根目录，默认本部署目录下 storage')
    args = parser.parse_args()
    try:
        directory = Path(args.directory).resolve()
        hardware = args.hardware
        if args.from_env:
            profile = os.environ.get('COMPOSE_PROFILES', '').strip()
            if profile not in {'cpu', 'intel'}:
                raise ValueError('COMPOSE_PROFILES 必须只选择 cpu 或 intel')
            hardware = {'cpu': 'epyc-cpu', 'intel': 'intel-igpu'}[profile]
        if not args.media_host:
            raise ValueError('请在 .env 填写 M0_MEDIA_HOST（服务器 IP）')
        if not hardware:
            raise ValueError('请选择硬件档案')
        try:
            rtc_port = int(os.environ.get('M0_RTC_PORT', '8000'))
        except ValueError:
            raise ValueError('M0_RTC_PORT 需为 1–65535') from None
        channels = initialize(directory, directory / args.cameras, hardware, args.media_host,
                              storage_root=args.storage_root, write_env=not args.no_env,
                              channels=parse_env_channels(os.environ) if args.from_env else None, rtc_port=rtc_port)
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        parser.exit(1, '初始化失败：' + str(error) + '\n')
    print(f'已生成 {len(channels)} 路 {hardware} 配置；登录信息见 state/operator.txt。')


if __name__ == '__main__':
    main()
