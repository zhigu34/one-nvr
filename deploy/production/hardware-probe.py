"""Isolated probe inside the exact Frigate image; no RTSP or recording source.
Sample: FFmpeg lavfi testsrc2 320x240/5fps, six H.264 frames. Record the
actual sample/model SHA256 and command provenance; do not infer camera capacity.
"""
import glob, hashlib, json, os, subprocess, sys, tempfile
from pathlib import Path

identifier, node = sys.argv[1:3]
result = {"decode": False, "inference": False, "reason": "not_tested"}
ffmpeg = next(iter(sorted(glob.glob('/usr/lib/ffmpeg/*/bin/ffmpeg'))), '/usr/bin/ffmpeg')

def execute(args):
    subprocess.run(args, check=True, timeout=35, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

if len(sys.argv)>3 and sys.argv[3]=='--inference':
    try:
        import numpy as np
        if identifier == 'cpu':
            try:
                from tflite_runtime.interpreter import Interpreter
            except ModuleNotFoundError:
                from tensorflow.lite.python.interpreter import Interpreter
            model = next(p for p in ['/cpu_model.tflite', '/models/cpu_model.tflite'] if os.path.isfile(p))
            result['model_sha256'] = hashlib.sha256(Path(model).read_bytes()).hexdigest()
            interpreter = Interpreter(model_path=model, num_threads=2)
            interpreter.allocate_tensors()
            value = interpreter.get_input_details()[0]
            interpreter.set_tensor(value['index'], np.zeros(value['shape'], dtype=value['dtype']))
            interpreter.invoke()
            assert all(np.isfinite(interpreter.get_tensor(x['index'])).all() for x in interpreter.get_output_details())
        else:
            from openvino import Core
            model = '/openvino-model/ssdlite_mobilenet_v2.xml'
            result['model_sha256'] = hashlib.sha256(Path(model).read_bytes()+Path(model.replace('.xml','.bin')).read_bytes()).hexdigest()
            core = Core()
            compiled = core.compile_model(model, 'GPU')
            tensor = compiled.input(0)
            output = compiled([np.zeros(tensor.shape, dtype=tensor.element_type.to_dtype())])
            assert all(np.isfinite(x).all() for x in output.values())
        result['inference'] = True
        result['reason'] = 'validated'
    except Exception:
        result['reason'] = 'inference_selftest_failed'
    print(json.dumps(result))
    sys.exit(0)

try:
    with tempfile.TemporaryDirectory() as root:
        sample = root + '/probe.mp4'
        execute([ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error', '-f', 'lavfi', '-i', 'testsrc2=size=320x240:rate=5', '-frames:v', '6', '-c:v', 'libx264', '-threads', '1', '-y', sample])
        result['sample_sha256'] = hashlib.sha256(Path(sample).read_bytes()).hexdigest()
        args = [ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error']
        if identifier != 'cpu':
            args += ['-hwaccel', 'vaapi', '-hwaccel_device', node, '-hwaccel_output_format', 'vaapi']
        args += ['-i', sample, '-frames:v', '6', '-f', 'null', '-']
        execute(args)
        result['decode'] = True
except Exception:
    result['reason'] = 'decode_selftest_failed'
# Bounded subprocess prevents a hung driver/model from hanging deployment.
try:
    child=subprocess.run([sys.executable,__file__,identifier,node,'--inference'],check=True,timeout=40,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True)
    stage=json.loads(child.stdout)
    result['inference']=stage['inference']
    if 'model_sha256' in stage: result['model_sha256']=stage['model_sha256']
    if result['decode'] and result['inference']: result['reason']='validated'
    elif not result['inference']: result['reason']='inference_selftest_failed'
except Exception:
    result['reason']='inference_selftest_timeout_or_failed'
print(json.dumps(result))
