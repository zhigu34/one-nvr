"""Shared, dependency-free camera configuration helpers for the M0 probe."""
import csv
import hashlib
import io
import ipaddress
import json
import re
from urllib.parse import quote, urlsplit

FIELDS = ['channel_no', 'channel_name', 'ip', 'rtsp_port', 'username', 'password', 'main_path', 'sub_path']


def parse_channels(text):
    if len(text.encode('utf-8')) > 1048576:
        raise ValueError('CSV 超过 1MiB')
    reader = csv.DictReader(io.StringIO(text.lstrip('\ufeff')))
    if not reader.fieldnames or not set(FIELDS).issubset(reader.fieldnames):
        raise ValueError('CSV 必须包含八列模板字段')
    if set(reader.fieldnames) - set(FIELDS) - {'onvif_port'}:
        raise ValueError('CSV 包含未知字段')
    if len(reader.fieldnames) != len(set(reader.fieldnames)):
        raise ValueError('CSV 表头重复')
    result, seen = [], set()
    for line, row in enumerate(reader, 2):
        if None in row or any(value is None for value in row.values()):
            raise ValueError(f'第 {line} 行列数不正确')
        if not any(row.values()):
            continue
        number = row['channel_no'].strip().upper()
        if not re.fullmatch(r'CH(?:0[1-9]|[12][0-9]|3[0-2])', number):
            raise ValueError(f'第 {line} 行需明确填写 CH01…CH32')
        if number in seen:
            raise ValueError(f'第 {line} 行目标通道重复')
        seen.add(number)
        try:
            address = str(ipaddress.ip_address(row['ip'].strip()))
            port = int(row['rtsp_port'].strip() or '554')
            onvif = int(row['onvif_port']) if row.get('onvif_port', '').strip() else None
        except ValueError:
            raise ValueError(f'第 {line} 行 IP 或端口无效') from None
        if not 1 <= port <= 65535 or (onvif is not None and not 1 <= onvif <= 65535):
            raise ValueError(f'第 {line} 行端口越界')
        main, sub = row['main_path'].strip(), row['sub_path'].strip()
        for path in [main, sub]:
            if path and (not path.startswith('/') or path.startswith('//') or '#' in path or any(ord(c) < 33 for c in path)):
                raise ValueError(f'第 {line} 行码流路径无效，需填写 /main 形式')
        if not main:
            raise ValueError(f'第 {line} 行 main_path 必填')
        if row['password'] in {'••••••', '******', '********'}:
            raise ValueError(f'第 {line} 行不能填写密码掩码')
        if any(ord(c) < 32 for c in row['username'] + row['password']):
            raise ValueError(f'第 {line} 行凭据含控制字符')
        channel = dict(channel_no=number, channel_name=row['channel_name'].strip() or number,
                       ip=address, rtsp_port=port, username=row['username'], password=row['password'],
                       main_path=main, sub_path=sub, onvif_port=onvif)
        # Source changes get distinct stream names, so old callbacks cannot become new media.
        identity = {key: channel[key] for key in FIELDS if key not in {'channel_name', 'channel_no'}}
        generation = hashlib.sha256(json.dumps(identity, sort_keys=True).encode()).hexdigest()[:12]
        channel.update(generation=generation, camera_key=number.lower() + '_' + generation,
                       main_stream=number.lower() + '_main_' + generation,
                       sub_stream=number.lower() + '_sub_' + generation)
        result.append(channel)
    if not result or len(result) > 32:
        raise ValueError('需配置 1–32 个通道，M0 建议先配置 2 个')
    return result


def build_rtsp(channel, path):
    address = channel['ip']
    if ':' in address:
        address = '[' + address + ']'
    auth = ''
    if channel['username'] or channel['password']:
        auth = quote(channel['username'], safe='') + ':' + quote(channel['password'], safe='') + '@'
    return f"rtsp://{auth}{address}:{channel['rtsp_port']}{path}"


def parse_env_channels(environment):
    """Read full URLs from camera1…camera32 without rewriting credentials/query."""
    keys = [key for key in environment if key.startswith('camera')]
    allowed = re.compile(r'camera([1-9]|[12][0-9]|3[0-2])(?:_(sub|name))?')
    for key in keys:
        if not allowed.fullmatch(key):
            raise ValueError('摄像头字段仅支持 camera1…camera32 及 _sub、_name')
    if sum(len(str(environment[key]).encode('utf-8')) for key in keys) > 1048576:
        raise ValueError('摄像头配置超过 1MiB')
    result = []
    for index in range(1, 33):
        key = f'camera{index}'
        main = environment.get(key, '').strip()
        sub = environment.get(key + '_sub', '').strip()
        name = environment.get(key + '_name', '').strip()
        if not main:
            if sub or name:
                raise ValueError(f'{key} 主码流 URL 必填')
            continue
        for suffix, url in [('', main), ('_sub', sub)]:
            if not url:
                continue
            try:
                parsed = urlsplit(url)
                port = parsed.port if parsed.port is not None else 554
                valid = parsed.scheme == 'rtsp' and bool(parsed.hostname) and 1 <= port <= 65535
                valid = valid and not parsed.fragment and not any(c.isspace() or ord(c) < 32 for c in url)
            except ValueError:
                valid = False
            if not valid:
                # Never include a URL or parser exception that may contain camera credentials.
                raise ValueError(f'{key}{suffix} 需填写有效的完整 rtsp:// URL')
        number = f'CH{index:02}'
        generation = hashlib.sha256(json.dumps([main, sub], ensure_ascii=False).encode()).hexdigest()[:12]
        result.append(dict(channel_no=number, channel_name=name or number,
                           main_url=main, sub_url=sub or None, generation=generation,
                           camera_key=number.lower() + '_' + generation,
                           main_stream=number.lower() + '_main_' + generation,
                           sub_stream=number.lower() + '_sub_' + generation))
    if not result:
        raise ValueError('需配置至少一个 camera1…camera32，M0 建议先配置 2 个')
    return result
