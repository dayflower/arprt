#!/usr/bin/env python3
"""Validate real PVC-backed render Jobs in an isolated namespace.

Reuses identical installed CRD schemas without modifying them; removes only
resources this run created. Requires kubectl and a cluster-accessible image.
"""
import argparse
import overlay_checks
import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('cluster_smoke', ROOT / 'hack/cluster-smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
kubectl, wait, forwarded = smoke.kubectl, smoke.wait, smoke.forwarded


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    args = parser.parse_args()
    namespace = 'arprt-phase4-' + uuid.uuid4().hex[:8]
    created_crds, created_namespace = [], False
    try:
        for name in smoke.CRDS:
            plural, group = name.split('.', 1)
            path = str(ROOT / 'config/crd/bases' / (group + '_' + plural + '.yaml'))
            existing = kubectl('get', 'crd', name, '--ignore-not-found', '-o', 'json').stdout.strip()
            if existing:
                expected = json.loads(kubectl('create', '--dry-run=client', '-f', path, '-o', 'json').stdout)['spec']
                actual = json.loads(existing)['spec']
                version = next(v for v in actual['versions'] if v['name'] == 'v1alpha1')
                if actual['group'] != expected['group'] or actual['scope'] != expected['scope'] or version['schema'] != expected['versions'][0]['schema']:
                    raise RuntimeError('installed CRD contract differs; refusing to modify it')
            else:
                kubectl('create', '-f', path)
                created_crds.append(name)
                kubectl('wait', '--for=condition=Established', 'crd/' + name, '--timeout=30s')
        kubectl('create', 'namespace', namespace)
        created_namespace = True
        with tempfile.TemporaryDirectory(prefix='arprt-job-smoke-') as temporary:
            directory = Path(temporary)

            def apply(obj):
                path = directory / 'resource.json'
                path.write_text(json.dumps(obj))
                kubectl('-n', namespace, 'apply', '--validate=strict', '-f', str(path))
                path.unlink()

            base = kubectl('kustomize', str(ROOT / 'config/default')).stdout
            # Kustomize namespace/image transformations preserve deployment and RBAC.
            (directory / 'base.yaml').write_text(base)
            image_name, image_tag = args.image.rsplit(':', 1)
            (directory / 'kustomization.yaml').write_text(json.dumps({
                'apiVersion': 'kustomize.config.k8s.io/v1beta1', 'kind': 'Kustomization',
                'namespace': namespace, 'resources': ['base.yaml'],
                'images': [{'name': 'arprt', 'newName': image_name, 'newTag': image_tag}],
                'patches': [{'target': {'kind': 'Deployment', 'name': 'arprt'}, 'patch': json.dumps([
                    {'op': 'replace', 'path': '/spec/template/spec/containers/0/args', 'value': [
                        '--namespace=$(POD_NAMESPACE)', '--job-pvc=arprt-data', '--job-deadline-seconds=30']}
                ])}],
            }))
            kubectl('apply', '--validate=strict', '-k', str(directory))
            kubectl('-n', namespace, 'rollout', 'status', 'deployment/arprt', '--timeout=180s')
            # A read-only observer inspects durable state; renderers never mount it.
            apply({'apiVersion': 'v1', 'kind': 'Pod', 'metadata': {'name': 'observer'}, 'spec': {
                'automountServiceAccountToken': False,
                'securityContext': {'runAsUser': 1000, 'runAsGroup': 1000, 'fsGroup': 1000},
                'affinity': {'podAffinity': {'requiredDuringSchedulingIgnoredDuringExecution': [{
                    'labelSelector': {'matchLabels': {'app.kubernetes.io/name': 'arprt'}},
                    'topologyKey': 'kubernetes.io/hostname'}]}},
                'containers': [{'name': 'observer', 'image': 'busybox:1.37.0', 'command': ['sleep', '3600'],
                    'volumeMounts': [{'name': 'data', 'mountPath': '/data', 'readOnly': True}]}],
                'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': 'arprt-data'}}]}})
            kubectl('-n', namespace, 'wait', '--for=condition=Ready', 'pod/observer', '--timeout=120s')
            image = base64.b64encode((ROOT / 'testdata/phase0/target-gray4.png').read_bytes()).decode()
            apply({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'input'}, 'data': {'IMAGE_PNG': image}})

            def screen(command, interval='5m', custom=False):
                source = {'renderer': {'image': 'busybox:1.37.0', 'command': ['sh', '-ec'], 'args': [command],
                    'envFrom': [{'configMapRef': {'name': 'input'}}]}}
                if custom:
                    source.update(contextPath='/input/context.json', imagePath='/work/weather.png')
                return {'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtScreen',
                    'metadata': {'name': 'screen'}, 'spec': {'refreshInterval': interval, 'source': {'job': source}}}

            normal = 'test -r /arprt/context.json; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; sleep 6; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'
            apply(screen(normal, '3s'))
            apply({'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtDevice',
                'metadata': {'name': 'living-room'}, 'spec': {'authSecretRef': {'name': 'living-room-auth'},
                    'screenRef': {'name': 'screen'}}})
            state_file = hashlib.sha256((namespace + '/living-room/screen').encode()).hexdigest() + '.json'

            def read(path):
                result = kubectl('-n', namespace, 'exec', 'observer', '--', 'cat', '/data/' + path, check=False)
                return result.stdout if result.returncode == 0 else ''

            def state():
                data = read('jobs/' + state_file)
                return json.loads(data) if data else {}

            def latest():
                result = kubectl('-n', namespace, 'exec', 'observer', '--', 'sh', '-c', 'cat /data/latest/*.json', check=False)
                if result.returncode != 0:
                    return {}
                # Multiple rotations retain separate latest-success records.
                decoder, remaining = json.JSONDecoder(), result.stdout.strip()
                current_unit = state().get('unit')
                while remaining:
                    record, end = decoder.raw_decode(remaining)
                    if record.get('unit') == current_unit:
                        return record
                    remaining = remaining[end:].lstrip()
                return {}

            def jobs():
                return json.loads(kubectl('-n', namespace, 'get', 'jobs', '-o', 'json').stdout)['items']

            if jobs():
                raise RuntimeError('Job started without an observed target')
            prefix = '/arprt/devices/' + namespace + '/living-room'
            headers = {'ID': '02:00:00:00:00:01', 'Model': 'og', 'FW-Version': '1.8.17',
                'Width': '800', 'Height': '480', 'Battery-Voltage': '3.91', 'RSSI': '-48'}

            def fetch(base, path):
                with urllib.request.urlopen(urllib.request.Request(base + path, headers=headers), timeout=10) as response:
                    return response.read()

            def display(base):
                started = time.monotonic()
                result = json.loads(fetch(base, prefix + '/api/display'))
                if time.monotonic() - started > 3:
                    raise RuntimeError('display waited for a Job')
                return result

            with forwarded(namespace, 8080, directory) as base:
                headers['Access-Token'] = json.loads(fetch(base, prefix + '/api/setup'))['api_key']
                if display(base) != {'status': 202}:
                    raise RuntimeError('initial Job did not return pending')
                wait(lambda: state().get('active'), 30)
                first_attempt = state()['active']['id']
                for _ in range(3):
                    display(base)
                if state()['active']['id'] != first_attempt or len(jobs()) != 1:
                    raise RuntimeError('polling duplicated active work')
            # Restart during renderer execution; Job identity and PVC survive.
            kubectl('-n', namespace, 'rollout', 'restart', 'deployment/arprt')
            kubectl('-n', namespace, 'rollout', 'status', 'deployment/arprt', '--timeout=180s')
            wait(lambda: latest().get('attemptID') == first_attempt, 60)
            first = latest()
            snapshot = read('runs/' + first_attempt + '/context.json')
            if hashlib.sha256(snapshot.encode()).hexdigest() != first['contextDigest'] or json.loads(snapshot)['device']['battery']['voltage'] != 3.91:
                raise RuntimeError('context digest or snapshot telemetry differs')
            controller = json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=arprt', '-o', 'json').stdout)['items'][0]
            # Subsequent interval attempt changes telemetry only at snapshot time.
            headers['Battery-Voltage'] = '3.70'
            with forwarded(namespace, 8080, directory) as base:
                result = display(base)
                path = result['image_url'].removeprefix('https://byos.example.test')
                if result['status'] != 0 or fetch(base, path) != (ROOT / 'testdata/phase0/target-gray4.png').read_bytes():
                    raise RuntimeError('authenticated Job image fetch failed')
                wait(lambda: latest().get('sequence', 0) > first['sequence'], 60)
                running = jobs()
                for job in running:
                    pods = json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'batch.kubernetes.io/job-name=' + job['metadata']['name'], '-o', 'json').stdout)['items']
                    for pod in pods:
                        if pod['spec'].get('nodeName') and pod['spec']['nodeName'] != controller['spec']['nodeName']:
                            raise RuntimeError('RWO renderer ran on another node')
                # Distinct parent paths and an unmodified existing busybox CLI.
                custom = 'test -r /input/context.json; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; printf %s "$IMAGE_PNG" | base64 -d > /work/weather.png'
                previous = latest()['sequence']
                apply(screen(custom, custom=True))
                wait(lambda: latest().get('sequence', 0) > previous, 60)
                success = latest()
                # Failed, missing, malformed and hanging executions retain success.
                for command, reason in [
                    ('printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png; exit 7', 'JobFailed'),
                    ('exit 0', 'CollectionFailed'),
                    ('printf partial > /arprt/output.png', 'CollectionFailed'),
                    ('sleep 90', 'JobTimedOut'),
                ]:
                    apply(screen(command))
                    wait(lambda: state().get('reason') == reason and not state().get('active'), 90)
                    if latest()['attemptID'] != success['attemptID'] or display(base)['status'] != 0:
                        raise RuntimeError('failed render replaced compatible success')
                # Make an old completeable render obsolete by replacing assignment.
                apply(screen('sleep 15; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'))
                wait(lambda: state().get('active'), 30)
                obsolete = state()['active']['id']
                kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--type=merge', '-p', json.dumps({'spec': {'screenRef': {'name': 'missing'}}}))
                wait(lambda: not state(), 60)
                wait(lambda: all(j['metadata']['name'] != 'arprt-render-' + obsolete for j in jobs()), 60)
                if latest().get('attemptID') == obsolete or display(base) != {'status': 202}:
                    raise RuntimeError('reassignment served obsolete output')
            # Complete a running Job while collection is offline, then recover
            # its durable run on controller startup without creating another Job.
            apply(screen('sleep 10; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'))
            kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--type=merge', '-p', json.dumps({'spec': {'screenRef': {'name': 'screen'}}}))
            wait(lambda: state().get('active'), 30)
            recovery = state()['active']['id']
            recovery_name = 'arprt-render-' + recovery

            def recovery_running():
                pods = json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'batch.kubernetes.io/job-name=' + recovery_name, '-o', 'json').stdout)['items']
                return any(p['status'].get('phase') == 'Running' for p in pods)

            wait(recovery_running, 30)
            kubectl('-n', namespace, 'scale', 'deployment/arprt', '--replicas=0')
            wait(lambda: not json.loads(kubectl('-n', namespace, 'get', 'pods', '-l', 'app.kubernetes.io/name=arprt', '-o', 'json').stdout)['items'], 60)
            kubectl('-n', namespace, 'wait', '--for=condition=Complete', 'job/' + recovery_name, '--timeout=45s')
            if latest().get('attemptID') == recovery:
                raise RuntimeError('offline collector unexpectedly published')
            kubectl('-n', namespace, 'scale', 'deployment/arprt', '--replicas=1')
            kubectl('-n', namespace, 'rollout', 'status', 'deployment/arprt', '--timeout=180s')
            wait(lambda: latest().get('attemptID') == recovery, 60)
            if state().get('active'):
                raise RuntimeError('completed recovery duplicated work')
            with forwarded(namespace, 8080, directory) as base:
                if display(base)['status'] != 0:
                    raise RuntimeError('recovered artifact not delivered')
            # Rotation schedules a fresh context, isolates old fallback and
            # preserves images already issued to the device.
            apply(screen('sleep 15; grep -q \'"rotation":90\' /arprt/context.json; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'))
            kubectl('-n', namespace, 'patch', 'arprtdevice', 'living-room', '--type=merge',
                    '-p', json.dumps({'spec': {'rotation': 90}}))
            wait(lambda: state().get('unit', {}).get('target', {}).get('rotation') == 90 and state().get('active'), 30)
            rotated_attempt = state()['active']['id']
            with forwarded(namespace, 8080, directory) as base:
                if display(base) != {'status': 202}:
                    raise RuntimeError('rotated Job borrowed another rotation fallback')
                if fetch(base, path) != (ROOT / 'testdata/phase0/target-gray4.png').read_bytes():
                    raise RuntimeError('rotation change revoked issued Job image')
                wait(lambda: latest().get('attemptID') == rotated_attempt, 60)
                snapshot = json.loads(read('runs/' + rotated_attempt + '/context.json'))
                if snapshot['renderTarget'] != {'width': 800, 'height': 480, 'palette': 'gray4', 'rotation': 90}:
                    raise RuntimeError('Job rotation or physical dimensions differ')
                if display(base)['status'] != 0:
                    raise RuntimeError('rotated Job output unavailable')
            # Display telemetry is independent of the completed Job's snapshot.
            original = latest()
            original_context = read('runs/' + original['attemptID'] + '/context.json')
            kubectl('-n', namespace, 'patch', 'arprtscreen', 'screen', '--type=merge', '-p',
                    json.dumps({'spec': {'overlay': {'enabled': True, 'items': ['battery', 'wifi'], 'margin': {'horizontal': 12, 'vertical': 4}}}}))
            headers['Percent-Charged'], headers['RSSI'] = '0', '-99'
            with forwarded(namespace, 8080, directory) as base:
                composite = display(base)
                if composite['status'] != 0:
                    raise RuntimeError('Job base overlay not delivered')
                composite_path = urllib.parse.urlsplit(composite['image_url']).path
                overlay_checks.assert_overlay(base64.b64decode(image), fetch(base, composite_path), width=40, height=18, rotation=90, margin_horizontal=12, margin_vertical=4)
            if latest() != original or read('runs/' + original['attemptID'] + '/context.json') != original_context:
                raise RuntimeError('compositing changed Job success or immutable context')
            account = 'system:serviceaccount:' + namespace + ':arprt'
            for ns, verb, resource, allowed in [(namespace, 'create', 'jobs', True), ('default', 'create', 'jobs', False),
                    (namespace, 'list', 'secrets', False), (namespace, 'create', 'pods', False)]:
                result = kubectl('auth', 'can-i', verb, resource, '-n', ns, '--as=' + account, check=False)
                if (result.stdout.strip() == 'yes') != allowed:
                    raise RuntimeError('RBAC scope mismatch')
            print('PASS: target gating, default/custom paths, no token, same-node RWO, snapshots, display overlays/base separation, rotation, interval refresh, polling independence, active restart, completed recovery, failures, timeout, stale output, obsolete reassignment and scoped RBAC')
    finally:
        if created_namespace:
            kubectl('delete', 'namespace', namespace, '--wait=true', '--timeout=120s')
        for name in created_crds:
            if not json.loads(kubectl('get', name, '--all-namespaces', '-o', 'json').stdout)['items']:
                kubectl('delete', 'crd', name)
        print('Disposable Job test resources removed; existing CRDs untouched')


if __name__ == '__main__':
    main()
