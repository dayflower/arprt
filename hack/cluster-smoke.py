#!/usr/bin/env python3
"""Exercise HTTP GET BYOS delivery on the current cluster in an isolated namespace.

Requires kubectl and a controller image available to the cluster. Reuses
identical installed CRDs and removes only resources created by this run.
"""
import argparse
import datetime
import overlay_checks
import base64
from contextlib import contextmanager
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
CRDS = ['arprtscreens.arprt.dayflower.github.io', 'arprtdevices.arprt.dayflower.github.io', 'arprtplaylists.arprt.dayflower.github.io']


def kubectl(*args, check=True):
    result = subprocess.run(['kubectl', *args], capture_output=True, text=True)
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result


def wait(check, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.5)
    raise RuntimeError('cluster check did not converge')


@contextmanager
def forwarded(namespace, port, directory):
    with (directory / ('forward-' + str(port))).open('w+') as log:
        process = subprocess.Popen(['kubectl', '-n', namespace, 'port-forward', 'deployment/arprt', ':' + str(port)], stdout=log, stderr=log)
        try:
            def ready():
                log.seek(0)
                return 'Forwarding from 127.0.0.1:' in log.read()
            wait(ready, 15)
            log.seek(0)
            local = next(line.split(' ->')[0].rsplit(':', 1)[1] for line in log.read().splitlines() if line.startswith('Forwarding from 127.0.0.1:'))
            yield 'http://127.0.0.1:' + local
        finally:
            process.terminate()
            process.wait(timeout=10)


def kustomize_image(image):
    """Keep registry ports intact and use Kustomize's digest field for indexes."""
    result = {'name': 'arprt'}
    if '@' in image:
        name, digest = image.rsplit('@', 1)
        if not digest.startswith('sha256:') or len(digest) != 71:
            raise ValueError('image digest must be sha256 with 64 hexadecimal digits')
        int(digest[7:], 16)
        result.update(newName=name, digest=digest)
    elif ':' in image.rsplit('/', 1)[-1]:
        name, tag = image.rsplit(':', 1)
        result.update(newName=name, newTag=tag)
    else:
        raise ValueError('smoke image must specify a tag or digest')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    namespace = 'arprt-phase2-' + uuid.uuid4().hex[:8]
    created_crds = []
    created_namespace = False
    try:
        for name in CRDS:
            plural, group = name.split('.', 1)
            path = str(ROOT / 'config/crd/bases' / (group + '_' + plural + '.yaml'))
            existing = kubectl('get', 'crd', name, '--ignore-not-found', '-o', 'json').stdout.strip()
            if existing:
                expected = json.loads(kubectl('create', '--dry-run=client', '-f', path, '-o', 'json').stdout)['spec']
                actual = json.loads(existing)['spec']
                version = next(v for v in actual['versions'] if v['name'] == 'v1alpha1')
                if actual['group'] != expected['group'] or actual['scope'] != expected['scope'] or version['schema'] != expected['versions'][0]['schema']:
                    raise RuntimeError('installed CRD contract differs; refusing to modify it')
                continue
            kubectl('create', '--validate=strict', '-f', path)
            created_crds.append(name)
            kubectl('wait', '--for=condition=Established', 'crd/' + name, '--timeout=30s')
        kubectl('create', 'namespace', namespace)
        created_namespace = True
        with tempfile.TemporaryDirectory(prefix='arprt-smoke-') as temporary:
            directory = Path(temporary)
            (directory / 'kustomization.yaml').write_text(json.dumps({
                'apiVersion': 'kustomize.config.k8s.io/v1beta1', 'kind': 'Kustomization', 'namespace': namespace,
                'resources': [os.path.relpath(ROOT / 'config/default', directory)],
                'images': [kustomize_image(args.image)],
            }))
            kubectl('apply', '--validate=strict', '-k', str(directory))
            kubectl('-n', namespace, 'create', 'configmap', 'renderer', '--from-file=renderer.py=' + str(ROOT / 'examples/http-get/renderer.py'))

            def apply(obj):
                file = directory / 'resource.json'
                file.write_text(json.dumps(obj))
                kubectl('-n', namespace, 'apply', '--validate=strict', '-f', str(file))
                file.unlink()

            apply({'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': {'name': 'renderer'}, 'spec': {
                'replicas': 1, 'selector': {'matchLabels': {'app': 'renderer'}}, 'template': {
                    'metadata': {'labels': {'app': 'renderer'}}, 'spec': {'automountServiceAccountToken': False,
                        'containers': [{'name': 'renderer', 'image': 'python:3.13-alpine', 'command': ['python3', '/app/renderer.py'],
                            'ports': [{'containerPort': 8000}], 'volumeMounts': [{'name': 'script', 'mountPath': '/app', 'readOnly': True}]}],
                        'volumes': [{'name': 'script', 'configMap': {'name': 'renderer'}}]}}}})
            apply({'apiVersion': 'v1', 'kind': 'Service', 'metadata': {'name': 'renderer'}, 'spec': {
                'selector': {'app': 'renderer'}, 'ports': [{'port': 8000, 'targetPort': 8000}]}})
            for sample in ['screens.yaml', 'device.yaml']:
                kubectl('-n', namespace, 'apply', '--validate=strict', '-f', str(ROOT / 'config/samples' / sample))
            kubectl('-n', namespace, 'patch', 'arprtscreen', 'photo', '--type=merge', '-p', json.dumps({'spec': {
                'source': {'http': {'url': 'http://renderer:8000/?encoding=indexed'}}, 'refreshInterval': '5m', 'overlay': None}}))
            # Secret reads are restricted to the supplied auth and MAC sources.
            account = 'system:serviceaccount:' + namespace + ':arprt'
            for ns, verb, resource, allowed in [
                (namespace, 'list', CRDS[0], True), (namespace, 'get', 'secret/living-room-auth', True),
                (namespace, 'get', 'secret/living-room-identity', True),
                (namespace, 'get', 'secret/unrelated', False), (namespace, 'list', 'secrets', False),
                (namespace, 'patch', 'secret/living-room-auth', True),
                (namespace, 'patch', 'secret/living-room-identity', False),
                (namespace, 'patch', 'secret/unrelated', False), (namespace, 'delete', 'secret/living-room-auth', False),
                (namespace, 'create', 'secrets', False), (namespace, 'update', 'secret/living-room-identity', False),
                ('default', 'patch', 'secret/living-room-auth', False),
                ('default', 'list', CRDS[0], False), (namespace, 'create', 'jobs', True),
            ]:
                answer = kubectl('auth', 'can-i', verb, resource, '--as=' + account, '-n', ns, check=False)
                if (answer.stdout.strip() == 'yes') != allowed:
                    raise RuntimeError('runtime RBAC did not match scoped permissions')
            patch_args = lambda values: kubectl('-n', namespace, 'patch', 'deployment', 'arprt', '--type=json', '-p', json.dumps([
                {'op': 'replace', 'path': '/spec/template/spec/containers/0/args', 'value': values}]))
            for deployment in ['renderer', 'arprt']:
                kubectl('-n', namespace, 'rollout', 'status', 'deployment/' + deployment, '--timeout=180s')
            prefix = '/arprt/devices/' + namespace + '/living-room'
            headers = {'ID': '02:00:00:00:00:01', 'Model': 'og', 'FW-Version': '1.8.17',
                       'Width': '800', 'Height': '480', 'Battery-Voltage': '3.91', 'RSSI': '-48'}

            def fetch(base, path, authenticated=True, request_headers=None):
                req = urllib.request.Request(base + path, headers=(request_headers or headers) if authenticated else {})
                try:
                    with urllib.request.urlopen(req, timeout=15) as response:
                        return response.status, response.headers, response.read()
                except urllib.error.HTTPError as response:
                    return response.code, response.headers, response.read()

            def display(base, path):
                status, _, body = fetch(base, path + '/api/display')
                value = json.loads(body)
                if status != 200 or value['status'] != 0:
                    raise RuntimeError('display did not deliver an image')
                if not value['image_url'].startswith('https://byos.example.test' + prefix + '/images/') or len(value['filename']) > 31:
                    raise RuntimeError('public image URL or firmware filename broken')
                return value

            with forwarded(namespace, 8081, directory) as base:
                for endpoint in ['healthz', 'readyz']:
                    if fetch(base, '/' + endpoint, False)[0] != 200:
                        raise RuntimeError('probe failed')
            with forwarded(namespace, 8080, directory) as base:
                if fetch(base, prefix + '/api/display')[0] != 401:
                    raise RuntimeError('display provisioned a key before setup')
                status, _, body = fetch(base, prefix + '/api/setup')
                token = json.loads(body).get('api_key', '')
                if status != 200 or len(base64.urlsafe_b64decode(token + '=')) != 32:
                    raise RuntimeError('pairing failed')
                headers['Access-Token'] = token
                stored_auth = json.loads(kubectl('-n', namespace, 'get', 'secret', 'living-room-auth', '-o', 'json').stdout)
                if base64.b64decode(stored_auth['data']['apiKey']).decode() != token:
                    raise RuntimeError('setup returned an unpersisted key')
                kubectl('-n', namespace, 'apply', '--validate=strict', '-f', str(ROOT / 'config/default/auth-secret.yaml'))
                if json.loads(fetch(base, prefix + '/api/setup')[2]).get('api_key') != token:
                    raise RuntimeError('GitOps re-apply reset the generated key')
                device = json.loads(kubectl('-n', namespace, 'get', 'arprtdevice', 'living-room', '-o', 'json').stdout)
                if device['status']['hardwareID'] != headers['ID'] or 'hardwareID' in device['spec']:
                    raise RuntimeError('automatic MAC registration rewrote spec or failed to record status')
                # Exercise all sources on live Kubernetes without changing source
                # selection after resource creation. All MACs are synthetic.
                for peer, identity in [
                    ('inline-peer', {'hardwareID': '02:00:00:00:00:04'}),
                    ('secret-peer', {'hardwareIDSecretRef': {'name': 'living-room-identity', 'key': 'mac'}}),
                    ('pending-peer', {}),
                ]:
                    apply({'apiVersion': device['apiVersion'], 'kind': device['kind'], 'metadata': {'name': peer},
                           'spec': {**device['spec'], **identity}})
                identity_secret = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'living-room-identity'},
                                   'stringData': {'mac': '02:00:00:00:00:02'}}
                apply(identity_secret)
                peer_prefix = prefix.rsplit('/', 1)[0]
                for peer, mac in [('inline-peer', '02:00:00:00:00:04'), ('secret-peer', '02:00:00:00:00:02')]:
                    peer_headers = {**headers, 'ID': mac}
                    status, _, body = fetch(base, peer_prefix + '/' + peer + '/api/setup', request_headers=peer_headers)
                    if status != 200 or json.loads(body)['api_key'] != token:
                        raise RuntimeError('configured MAC setup failed')
                if fetch(base, peer_prefix + '/pending-peer/api/setup')[0] != 503:
                    raise RuntimeError('duplicate automatic registration succeeded')
                pending = json.loads(kubectl('-n', namespace, 'get', 'arprtdevice', 'pending-peer', '-o', 'json').stdout)
                if pending.get('status', {}).get('hardwareID'):
                    raise RuntimeError('failed setup registered a duplicate MAC')
                identity_secret['stringData']['mac'] = '02:00:00:00:00:03'
                apply(identity_secret)
                for mac in ['02:00:00:00:00:02', '02:00:00:00:00:03']:
                    if fetch(base, peer_prefix + '/secret-peer/api/display', request_headers={**headers, 'ID': mac})[0] != 403:
                        raise RuntimeError('Secret change replaced the registered MAC')
                identity_secret['stringData']['mac'] = '02:00:00:00:00:02'
                apply(identity_secret)
                kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--subresource=status', '--type=merge',
                        '-p', json.dumps({'status': {'hardwareID': None}}))
                if fetch(base, prefix + '/api/setup', request_headers={**headers, 'ID': '02:00:00:00:00:05'})[0] != 403:
                    raise RuntimeError('status loss allowed replacement of the PVC binding')
                if fetch(base, prefix + '/api/setup')[0] != 200:
                    raise RuntimeError('setup did not recover the status mirror')
                status, image_headers, logo = fetch(base, prefix + '/setup-logo.png', False)
                if status != 200 or len(logo) != int(image_headers['Content-Length']):
                    raise RuntimeError('anonymous setup image failed')
                render_start = datetime.datetime.now(datetime.timezone.utc)
                first = display(base, prefix)
                render_end = datetime.datetime.now(datetime.timezone.utc)
                image_path = first['image_url'].removeprefix('https://byos.example.test')
                status, image_headers, image = fetch(base, image_path)
                if status != 200 or len(image) != int(image_headers['Content-Length']) or image_headers['Content-Type'] != 'image/png':
                    raise RuntimeError('authenticated image fetching failed')
                if fetch(base, image_path, False)[0] != 403:
                    raise RuntimeError('anonymous display image accepted')
                if display(base, prefix) != first:
                    raise RuntimeError('fresh artifact was not reused')
                for rotation in (90, 180, 270):
                    kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--type=merge',
                            '-p', json.dumps({'spec': {'rotation': rotation}}))
                    rotated = display(base, prefix)
                    rotated_path = rotated['image_url'].removeprefix('https://byos.example.test')
                    if rotated['status'] != 0 or fetch(base, rotated_path)[2] == image:
                        raise RuntimeError('GET renderer did not apply desired rotation')
                    if display(base, prefix) != rotated:
                        raise RuntimeError('rotated GET output was not reused')
                    if fetch(base, image_path)[2] != image:
                        raise RuntimeError('rotation change revoked an issued image')
                kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--type=merge',
                        '-p', json.dumps({'spec': {'rotation': 0}}))
                if display(base, prefix) != first:
                    raise RuntimeError('default rotation lost original compatible output')
            state = json.loads(kubectl('-n', namespace, 'get', 'arprtdevice', 'living-room', '-o', 'json').stdout)['status']
            if state['battery']['voltage'] != 3.91 or state['network']['rssi'] != -48 or token in json.dumps(state):
                raise RuntimeError('telemetry or credential exclusion failed')
            # Restart with prefix stripping and a renderer outage. PVC issuance and
            # stale success must survive; generated URLs retain the external prefix.
            kubectl('-n', namespace, 'scale', 'deployment/renderer', '--replicas=0')
            wait(lambda: not json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'app=renderer', '-o', 'json').stdout)['items'])
            kubectl('-n', namespace, 'patch', 'arprtscreen', 'photo', '--type=merge', '-p', json.dumps({'spec': {'refreshInterval': '1s'}}))
            patch_args(['--namespace=$(POD_NAMESPACE)', '--job-pvc=arprt-data', '--proxy-strips-prefix=true', '--overlay-time-zone=Asia/Tokyo'])
            kubectl('-n', namespace, 'rollout', 'status', 'deployment/arprt', '--timeout=180s')
            stripped = prefix.removeprefix('/arprt')
            with forwarded(namespace, 8080, directory) as base:
                if fetch(base, image_path.removeprefix('/arprt'))[2] != image:
                    raise RuntimeError('image or issuance lost on restart')
                if display(base, stripped) != first:
                    raise RuntimeError('outage failed to preserve the last compatible image')
                status, _, body = fetch(base, stripped + '/api/setup')
                if status != 200 or json.loads(body).get('api_key') != token:
                    raise RuntimeError('setup availability or key lost after restart')
                if fetch(base, stripped + '/api/display', request_headers={**headers, 'ID': '02:00:00:00:00:05'})[0] != 403:
                    raise RuntimeError('PVC restart lost the registered MAC')
                # Draw from stale renderer bytes, using embedded zoneinfo in scratch.
                kubectl('-n', namespace, 'patch', 'arprtscreen', 'photo', '--type=merge', '-p',
                        json.dumps({'spec': {'overlay': {'enabled': True, 'margin': {'horizontal': 12, 'vertical': 4}}}}))
                composite = display(base, stripped)
                composite_path = composite['image_url'].removeprefix('https://byos.example.test/arprt')
                composite_bytes = fetch(base, composite_path)[2]
                overlay_checks.assert_overlay(image, composite_bytes, margin_horizontal=12, margin_vertical=4)
                shown = datetime.datetime.strptime(overlay_checks.timestamp(composite_bytes, margin_horizontal=12, margin_vertical=4), '%Y-%m-%d %H:%M')
                shown = shown.replace(tzinfo=datetime.timezone(datetime.timedelta(hours=9))).astimezone(datetime.timezone.utc)
                if not render_start.replace(second=0, microsecond=0) <= shown <= render_end.replace(second=0, microsecond=0):
                    raise RuntimeError('overlay lost original timestamp or scratch IANA timezone')
                headers['Percent-Charged'] = '20.5'
                fractional = display(base, stripped)
                if fractional['filename'] == composite['filename']:
                    raise RuntimeError('current fractional percentage did not change battery icon')
                status = json.loads(kubectl('-n', namespace, 'get', 'arprtdevice', 'living-room', '-o', 'json').stdout)['status']
                if status['battery']['percent'] != 20.5:
                    raise RuntimeError('fractional percentage lost in status')
                # USB presence alone never establishes active charging.
                headers['USB-Connected'] = 'true'
                if display(base, stripped)['filename'] != fractional['filename']:
                    raise RuntimeError('USB inferred active charging')
                headers['Battery-Charging'] = 'invalid'
                if display(base, stripped)['filename'] != fractional['filename']:
                    raise RuntimeError('invalid charging fell back to USB')
                headers['Battery-Charging'] = '1'
                charging = display(base, stripped)
                charging_bytes = fetch(base, charging['image_url'].removeprefix('https://byos.example.test/arprt'))[2]
                overlay_checks.assert_overlay(image, charging_bytes, margin_horizontal=12, margin_vertical=4)
                if charging['filename'] == fractional['filename']:
                    raise RuntimeError('known charging mark did not replace the level')
                headers['USB-Connected'] = 'false'
                if display(base, stripped)['filename'] != charging['filename']:
                    raise RuntimeError('USB changed known charging mark identity')
                headers['USB-Connected'] = 'true'
                headers['Battery-Charging'] = '0'
                if display(base, stripped)['filename'] != fractional['filename']:
                    raise RuntimeError('USB overrode explicit not-charging state')
                headers.pop('USB-Connected')
                headers.pop('Battery-Charging')
                kubectl('-n', namespace, 'patch', 'arprtscreen', 'photo', '--type=merge', '-p',
                        json.dumps({'spec': {'overlay': {'scale': 100}}}))
                if display(base, stripped) != first or fetch(base, composite_path)[2] != composite_bytes:
                    raise RuntimeError('layout fallback or issued composite retention failed')
                # Changing UID while the renderer is down must yield initial pending.
                screen = json.loads(kubectl('-n', namespace, 'get', 'arprtscreen', 'photo', '-o', 'json').stdout)
                kubectl('-n', namespace, 'delete', 'arprtscreen', 'photo')
                apply({'apiVersion': screen['apiVersion'], 'kind': screen['kind'], 'metadata': {'name': 'photo'}, 'spec': screen['spec']})
                status, _, body = fetch(base, stripped + '/api/display')
                if status != 200 or json.loads(body) != {'status': 202}:
                    raise RuntimeError('replacement resource borrowed an obsolete image')
            print('PASS: generated/stable key, default setup, GitOps re-apply, three MAC sources, duplicates, Secret changes, status recovery, both prefix modes, display, PNG fetch, auth, telemetry, rotation, reuse, scratch timezone, overlays/fractional telemetry/fallback, PVC restart, outage, initial pending and scoped RBAC')
    finally:
        if created_namespace:
            kubectl('delete', 'namespace', namespace, '--wait=true', '--timeout=120s')
        for name in created_crds:
            remaining = json.loads(kubectl('get', name, '--all-namespaces', '-o', 'json').stdout)
            if not remaining['items']:
                kubectl('delete', 'crd', name, '--wait=true', '--timeout=30s')
        print('Test namespace and unused test CRDs removed')


if __name__ == '__main__':
    main()
