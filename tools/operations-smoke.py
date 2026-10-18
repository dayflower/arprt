#!/usr/bin/env python3
"""Exercise documented examples, storage faults and metrics in an isolated namespace.

Requires kubectl and a cluster-accessible controller image. Never targets an
existing device namespace; uses a temporary administrative PVC inspection Pod.
"""
import argparse
import overlay_checks
import helm_install
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import urllib.request
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('cluster_smoke', ROOT / 'tools/cluster-smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
kubectl, wait, forwarded = smoke.kubectl, smoke.wait, smoke.forwarded


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    helm_install.arguments(parser)
    args = parser.parse_args()
    namespace = 'arprt-operations-' + uuid.uuid4().hex[:8]
    with tempfile.TemporaryDirectory(prefix='arprt-operations-') as temporary:
        directory = Path(temporary)
        installation = helm_install.Installation(args, namespace, directory)
        try:
            installation.install()

            for example in ['http/gray-bands', 'http/device-status', 'job/gray-bands']:
                helm_install.apply_example(namespace, example)
            for name in ['arprt-arprt', 'example-renderer', 'device-status-renderer']:
                kubectl('-n', namespace, 'rollout', 'status', 'deployment/' + name, '--timeout=180s')

            def resource(kind, name):
                return json.loads(kubectl('-n', namespace, 'get', kind, name, '-o', 'json').stdout)

            def patch(kind, name, value):
                kubectl('-n', namespace, 'patch', kind, name, '--type=merge', '-p', json.dumps(value))

            kubectl('-n', namespace, 'apply', '-f', str(ROOT / 'examples/custom-resources/device.yaml'))
            headers = {'ID': '02:00:00:00:00:01', 'Model': 'og', 'FW-Version': '1.8.17', 'Width': '800', 'Height': '480', 'Battery-Voltage': '3.91'}
            path = '/arprt/devices/' + namespace + '/living-room'

            def fetch(base, route):
                with urllib.request.urlopen(urllib.request.Request(base + route, headers=headers), timeout=15) as response:
                    return response.read()

            with forwarded(namespace, 8080, directory) as base:
                headers['Access-Token'] = json.loads(fetch(base, path + '/api/setup'))['api_key']

            def display():
                with forwarded(namespace, 8080, directory) as base:
                    return json.loads(fetch(base, path + '/api/display'))

            def ready_display():
                result = display()
                return result if result['status'] == 0 else None

            for screen in ['photo', 'device-status', 'job-bands', 'job-bands-custom']:
                patch('arprtdevice', 'living-room', {'spec': {'screenRef': {'name': screen}}})
                wait(ready_display, 90)
                result = ready_display()
                with forwarded(namespace, 8080, directory) as base:
                    png = fetch(base, urllib.parse.urlsplit(result['image_url']).path)
                    assert png.startswith(b'\x89PNG\r\n\x1a\n') and len(png) < 90000
            print('PASS: clean installation and all documented GET/POST/default/custom Job examples', flush=True)

            # This administrative test Pod is the only full-root storage inspector.
            pod = {'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'storage-check'}, 'spec': {
                'automountServiceAccountToken': False,
                'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'fsGroup': 1000},
                'containers': [{'name': 'inspect', 'image': 'busybox:1.37.0', 'command': ['sleep', '3600'],
                                'volumeMounts': [{'name': 'data', 'mountPath': '/data'}]}],
                'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': 'arprt-arprt-data'}}]}}
            controller_pods = json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=arprt', '-o', 'json').stdout)['items']
            pod['spec']['nodeName'] = controller_pods[0]['spec']['nodeName']
            (directory / 'inspector.json').write_text(json.dumps(pod))
            kubectl('-n', namespace, 'apply', '-f', str(directory / 'inspector.json'))
            kubectl('-n', namespace, 'wait', '--for=condition=Ready', 'pod/storage-check', '--timeout=60s')

            def inspect(script):
                return kubectl('-n', namespace, 'exec', 'storage-check', '--', 'sh', '-ec', script).stdout

            patch('arprtdevice', 'living-room', {'spec': {'screenRef': {'name': 'photo'}}})
            result = ready_display()
            image_route = path + '/images/' + result['image_url'].rsplit('/', 1)[-1]
            screen = resource('arprtscreen', 'photo')
            unit = {'namespace': namespace, 'screenUID': screen['metadata']['uid'], 'target': {'width': 800, 'height': 480, 'palette': 'gray4'}}
            unit['output'] = {'pngByteLimit': 90000, 'compatibility': 'output-v1'}
            key = hashlib.sha256(json.dumps(unit, separators=(',', ':')).encode()).hexdigest()
            inspect('printf broken > /data/latest/' + key + '.json')
            assert ready_display()['image_url'] == result['image_url']
            image_digest = result['image_url'].rsplit('/', 1)[-1][:-4]
            inspect('printf broken > /data/blobs/' + image_digest + '.png')
            assert ready_display()['image_url'] == result['image_url']
            print('PASS: corrupt pointer and blob regenerate from validated renderer bytes', flush=True)

            # Derived output must not replace base latest and must survive reassignment/GC.
            base_record = json.loads(inspect('cat /data/latest/' + key + '.json'))
            with forwarded(namespace, 8080, directory) as base:
                original_bytes = fetch(base, image_route)
                patch('arprtscreen', 'photo', {'spec': {'overlay': {'enabled': True, 'items': ['battery', 'wifi'], 'margin': {'top': 4, 'bottom': 4, 'left': 12, 'right': 12}}}})
                composite = json.loads(fetch(base, path + '/api/display'))
                composite_route = urllib.parse.urlsplit(composite['image_url']).path
                composite_bytes = fetch(base, composite_route)
                overlay_checks.assert_overlay(original_bytes, composite_bytes, width=40, height=18, margin_top=4, margin_bottom=4, margin_left=12, margin_right=12)
                # Definition edit may refresh the base, but the pointer must still identify renderer bytes.
                current_base = json.loads(inspect('cat /data/latest/' + key + '.json'))
                assert current_base['imageDigest'] == base_record['imageDigest']
                assert composite['image_url'].rsplit('/', 1)[-1][:-4] != current_base['imageDigest']
                patch('arprtdevice', 'living-room', {'spec': {'screenRef': {'name': 'device-status'}}})
                assert fetch(base, composite_route) == composite_bytes
            patch('arprtdevice', 'living-room', {'spec': {'screenRef': {'name': 'photo'}}})
            patch('arprtscreen', 'photo', {'spec': {'overlay': None}})
            installation.settings['storage'] = {'maxBytes': 1}
            installation.upgrade()
            patch('arprtscreen', 'photo', {'spec': {'refreshInterval': '1s'}})
            assert ready_display()['image_url'] == result['image_url']
            conditions = {c['type']: c for c in resource('arprtdevice', 'living-room')['status']['conditions']}
            assert conditions['Ready']['reason'] == 'StaleImageAvailable'
            with forwarded(namespace, 8080, directory) as base:
                assert fetch(base, image_route).startswith(b'\x89PNG')
            with forwarded(namespace, 8082, directory) as base:
                metrics = urllib.request.urlopen(base + '/metrics', timeout=10).read().decode()
                assert 'arprt_storage_errors_total{operation="capacity"}' in metrics
                for sensitive in [headers['ID'], headers['Access-Token'], 'living-room', 'screenUID', 'contextDigest']:
                    assert sensitive not in metrics, 'sensitive/high-cardinality metrics'
            installation.settings.pop('storage')
            installation.upgrade()
            assert ready_display()['status'] == 0
            print('PASS: capacity admission preserves stale success and issued URL; metrics redact identities', flush=True)

            # HTTP-only startup must also collect abandoned data without touching issued bytes.
            orphan = 'f' * 64
            inspect('printf orphan > /data/blobs/' + orphan + '.png; touch -d 2020-01-01 /data/blobs/' + orphan + '.png')
            installation.settings['jobs'] = {'enabled': False}
            installation.upgrade()
            wait(lambda: inspect('test ! -e /data/blobs/' + orphan + '.png && printf removed || true') == 'removed', 75)
            with forwarded(namespace, 8080, directory) as base:
                assert fetch(base, image_route).startswith(b'\x89PNG')
            logs = kubectl('-n', namespace, 'logs', 'deployment/arprt-arprt').stdout
            assert headers['Access-Token'] not in logs and headers['ID'] not in logs
            with forwarded(namespace, 8080, directory) as base:
                assert fetch(base, composite_route) == composite_bytes
            print('PASS: graceful restart, HTTP-only GC, base/composite issued URL retention and redacted logs', flush=True)
        finally:
            installation.cleanup()
            print('Disposable Helm test resources removed; existing CRDs untouched')


if __name__ == '__main__':
    main()
