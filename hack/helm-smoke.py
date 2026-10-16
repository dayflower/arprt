#!/usr/bin/env python3
"""Exercise packaged Helm releases in a dedicated disposable kind cluster.

Never reads the operator's kubeconfig. Schema-upgrade fixtures are isolated from
production charts and the matching-schema smoke suites.
"""
import argparse
import helm_install
import base64
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import tarfile
import tempfile
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('cluster_smoke', ROOT / 'hack/cluster-smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)
kubectl, wait = smoke.kubectl, smoke.wait


def run(*args, check=True, env=None):
    result = subprocess.run([str(arg) for arg in args], capture_output=True, text=True, env=env)
    if check and result.returncode:
        # Helm manifests and values contain no keys/MACs; never request Secrets
        # or controller logs as failure diagnostics.
        raise RuntimeError(f'{args[0]} failed: {result.stderr.strip() or result.stdout.strip()}')
    return result


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def image_values(image):
    return helm_install.image_values(image)


def packages(args, directory, env):
    destination = directory / 'packages'
    destination.mkdir()
    if args.oci_repository:
        require(not args.load_image, 'published acceptance must pull images anonymously')
        require('@sha256:' in args.image, 'published acceptance requires the image index digest')
        require(args.oci_repository.startswith('oci://') and args.chart_version,
                'published acceptance requires OCI repository and chart version')
        for name in ('arprt', 'arprt-crds'):
            run(args.helm, 'pull', args.oci_repository.rstrip('/') + '/' + name,
                '--version', args.chart_version, '--destination', destination, env=env)
    else:
        require(args.packages, 'local acceptance requires packaged charts')
        for archive in Path(args.packages).glob('*.tgz'):
            (destination / archive.name).write_bytes(archive.read_bytes())
    selected = {}
    for name in ('arprt', 'arprt-crds'):
        matches = [path for path in destination.glob(name + '-*.tgz')
                   if re.fullmatch(re.escape(name) + r'-[0-9].*\.tgz', path.name)]
        require(len(matches) == 1, f'expected exactly one {name} package; clean the output directory')
        selected[name] = matches[0]
    return selected


def upgrade_fixture(helm, archive, directory, env):
    """Add a compatible field to only an unpacked parent dependency."""
    fixture = directory / 'upgrade'
    fixture.mkdir()
    with tarfile.open(archive) as source:
        source.extractall(fixture, filter='data')
    chart = fixture / 'arprt'
    files = list(chart.rglob('arprt.dayflower.github.io_arprtplaylists.yaml'))
    require(len(files) == 1, 'packaged parent must contain its CRD dependency')
    path = files[0]
    crd = json.loads(kubectl('create', '--dry-run=client', '-f', str(path), '-o', 'json').stdout)
    crd['spec']['versions'][0]['schema']['openAPIV3Schema']['properties']['spec']['properties']['optionalUpgradeField'] = {'type': 'string'}
    path.write_text(json.dumps(crd))
    # A real schema change, not just a chart version bump. Never regenerate or
    # modify the production API; keep this copy until the cluster is deleted.
    metadata = chart / 'Chart.yaml'
    metadata.write_text(re.sub(r'^version: .*$', 'version: 0.1.1-smoke', metadata.read_text(), flags=re.M))
    run(helm, 'package', chart, '--destination', fixture, env=env)
    return fixture / 'arprt-0.1.1-smoke.tgz'


class Lifecycle:
    def __init__(self, args, directory, archives, env, separate):
        self.args, self.directory, self.archives, self.env = args, directory, archives, env
        self.separate = separate
        self.namespace = 'custom-space' if separate else 'arprt'
        self.release = 'home' if separate else 'arprt'
        self.name = self.release + '-arprt'
        self.claim = self.name + '-data'
        self.values = directory / ('separate.json' if separate else 'bundled.json')
        self.settings = {'byos': {'publicBaseURL': 'https://byos.example.test/arprt'},
                         'image': image_values(args.image), 'crds': {'enabled': not separate},
                         'credentials': {'authSecrets': [{'name': 'living-room-auth', 'create': True}],
                                         'identitySecrets': ['living-room-identity']}}

    def helm(self, *args):
        return run(self.args.helm, *args, '--namespace', self.namespace, env=self.env)

    def apply(self, obj, check=True):
        path = self.directory / 'resource.json'
        path.write_text(json.dumps(obj))
        try:
            return kubectl('-n', self.namespace, 'apply', '--validate=strict', '-f', str(path), check=check)
        finally:
            path.unlink()

    def get(self, kind, name=None):
        return json.loads(kubectl('-n', self.namespace, 'get', kind, *([name] if name else []), '-o', 'json').stdout)

    def install(self, chart, operation='install'):
        self.values.write_text(json.dumps(self.settings))
        self.helm(operation, self.release, chart, '--values', self.values,
                  '--wait', '--timeout', '180s')
        kubectl('-n', self.namespace, 'rollout', 'status', 'deployment/' + self.name, '--timeout=180s')

    def forward(self):
        # Forward through the actual release-specific Service to test selectors.
        return smoke.forwarded(self.namespace, 8080, self.directory, 'service/' + self.name)

    def crds(self):
        return {name: self.get('crd', name) for name in smoke.CRDS}

    def snapshot(self):
        resources = [('pvc', self.claim), ('secret', 'living-room-auth'),
                     ('arprtdevice', 'living-room'), ('arprtscreen', 'http'), ('arprtscreen', 'job')]
        result = {(kind, name): self.get(kind, name)['metadata']['uid'] for kind, name in resources}
        result['key'] = self.get('secret', 'living-room-auth')['data']['apiKey']
        result['identity'] = self.get('arprtdevice', 'living-room')['status']['hardwareID']
        result['external'] = self.get('secret', 'living-room-identity')
        result['crds'] = {name: value['metadata']['uid'] for name, value in self.crds().items()}
        return result

    def assert_state(self, snapshot):
        require(self.snapshot() == snapshot, 'upgrade/reinstall changed CR, PVC, credential or identity state')
        with self.forward() as base:
            status, _, body = smoke.request(base, self.issued_path, self.headers)
            require(status == 200 and body == self.issued_image, 'previously issued image URL did not survive')
            status, _, body = smoke.request(base, self.prefix + '/api/setup', self.headers)
            require(status == 200 and json.loads(body).get('api_key') == self.token, 'setup did not reuse the existing key')
            unauthenticated = {name: value for name, value in self.headers.items() if name != 'Access-Token'}
            require(smoke.request(base, self.issued_path, unauthenticated)[0] == 401, 'image fetch allowed missing authentication')

    def display(self, base):
        status, _, body = smoke.request(base, self.prefix + '/api/display', self.headers)
        require(status == 200, 'display HTTP status differs')
        value = json.loads(body)
        require(value.get('status') in (0, 202), 'unexpected display protocol response')
        if value['status'] == 0:
            path = urllib.parse.urlsplit(value['image_url']).path
            status, _, image = smoke.request(base, path, self.headers)
            require(status == 200 and image.startswith(b'\x89PNG\r\n\x1a\n'), 'authenticated PNG fetch failed')
        return value

    def permissions(self):
        account = 'system:serviceaccount:' + self.namespace + ':' + self.name
        for namespace, verb, resource, allowed in [
            (self.namespace, 'get', 'secret/living-room-auth', True),
            (self.namespace, 'patch', 'secret/living-room-auth', True),
            (self.namespace, 'get', 'secret/living-room-identity', True),
            (self.namespace, 'patch', 'secret/living-room-identity', False),
            (self.namespace, 'get', 'secret/undeclared', False),
            (self.namespace, 'patch', 'secret/undeclared', False),
            (self.namespace, 'list', 'secrets', False), (self.namespace, 'create', 'secrets', False),
            ('default', 'get', 'secret/living-room-auth', False),
            ('default', 'list', smoke.CRDS[0], False), ('default', 'create', 'jobs', False),
            (self.namespace, 'create', 'jobs', True),
        ]:
            answer = kubectl('auth', 'can-i', verb, resource, '--as=' + account, '-n', namespace, check=False)
            require((answer.stdout.strip() == 'yes') == allowed, 'Helm RBAC permissions differ')

    def prepare_delivery(self):
        # Reuse the real HTTP example; it stays outside the Helm release.
        renderer = ROOT / 'examples/http-get'
        kubectl('-n', self.namespace, 'create', 'configmap', 'example-renderer',
                '--from-file=renderer.py=' + str(renderer / 'renderer.py'))
        kubectl('-n', self.namespace, 'apply', '--validate=strict', '-f', str(renderer / 'renderer.yaml'))
        kubectl('-n', self.namespace, 'rollout', 'status', 'deployment/example-renderer', '--timeout=180s')
        self.apply({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'living-room-identity'}, 'type': 'Opaque'})
        self.apply({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'input'},
                    'data': {'IMAGE_PNG': base64.b64encode((ROOT / 'testdata/phase0/target-gray4.png').read_bytes()).decode()}})
        for name, source in [
            ('http', {'http': {'url': 'http://example-renderer:8000/?encoding=indexed'}}),
            ('job', {'job': {'renderer': {'image': 'busybox:1.37.0', 'command': ['sh', '-ec'],
                     'args': ['test -r /arprt/context.json; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; sleep 6; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'],
                     'envFrom': [{'configMapRef': {'name': 'input'}}]}}}),
        ]:
            self.apply({'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtScreen',
                        'metadata': {'name': name}, 'spec': {'refreshInterval': '1h', 'source': source}})
        self.apply({'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtDevice',
                    'metadata': {'name': 'living-room'}, 'spec': {'authSecretRef': {'name': 'living-room-auth'}, 'screenRef': {'name': 'http'}}})
        self.prefix = '/arprt/devices/' + self.namespace + '/living-room'
        self.headers = {'ID': '02:00:00:00:00:01', 'Model': 'og', 'FW-Version': '1.8.17', 'Width': '800', 'Height': '480'}
        with self.forward() as base:
            require(smoke.request(base, self.prefix + '/api/display', self.headers)[0] == 401, 'display provisioned key before setup')
            status, _, body = smoke.request(base, self.prefix + '/api/setup', self.headers)
            self.token = json.loads(body).get('api_key', '')
            require(status == 200 and len(self.token) > 32, 'setup did not provision a key')
            self.headers['Access-Token'] = self.token
            wait(lambda: self.display(base)['status'] == 0, 90)
            issued = self.display(base)
            self.issued_path = urllib.parse.urlsplit(issued['image_url']).path
            self.issued_image = smoke.request(base, self.issued_path, self.headers)[2]
        self.apply({'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtDevice',
                    'metadata': {'name': 'living-room'}, 'spec': {'authSecretRef': {'name': 'living-room-auth'}, 'screenRef': {'name': 'job'}}})
        with self.forward() as base:
            self.display(base)
            wait(lambda: self.get('jobs')['items'], 60)
            controller = self.get('pods', '-l=app.kubernetes.io/instance=' + self.release)['items'][0]
            jobs = self.get('jobs')['items']
            for job in jobs:
                spec = job['spec']['template']['spec']
                volumes = [volume for volume in spec['volumes'] if 'persistentVolumeClaim' in volume]
                require(volumes and all(volume['persistentVolumeClaim']['claimName'] == self.claim for volume in volumes), 'Job PVC reference differs')
                selector = '-l=batch.kubernetes.io/job-name=' + job['metadata']['name']
                def scheduled():
                    pods = self.get('pods', selector)['items']
                    return pods and all(pod['spec'].get('nodeName') == controller['spec']['nodeName'] for pod in pods)
                wait(scheduled, 60)
            wait(lambda: self.display(base)['status'] == 0, 120)
            value = self.display(base)
            require(smoke.request(base, urllib.parse.urlsplit(value['image_url']).path, self.headers)[2] ==
                    (ROOT / 'testdata/phase0/target-gray4.png').read_bytes(), 'real Job did not publish its image')
        wait(lambda: all(job.get('status', {}).get('succeeded') for job in self.get('jobs')['items']), 90)

    def exercise(self):
        print(f'Checking {"separate" if self.separate else "bundled"} release {self.release}/{self.namespace}', flush=True)
        kubectl('create', 'namespace', self.namespace)
        if self.separate:
            self.helm('install', 'schemas', self.archives['arprt-crds'], '--wait', '--timeout', '60s')
        self.install(self.archives['arprt'])
        for name in smoke.CRDS:
            kubectl('wait', '--for=condition=Established', 'crd/' + name, '--timeout=30s')
        original_crds = self.crds()
        self.permissions()
        self.prepare_delivery()
        before = self.snapshot()
        previous = self.get('deployment', self.name)['spec']['template']['metadata']['annotations']['checksum/config']
        self.settings['display'] = {'deviceRefreshSeconds': 600, 'overlayTimeZone': 'Asia/Tokyo'}
        self.install(self.archives['arprt'], 'upgrade')
        require(self.get('deployment', self.name)['spec']['template']['metadata']['annotations']['checksum/config'] != previous, 'config upgrade did not trigger a rollout')
        self.assert_state(before)
        print('PASS: config rollout preserves credentials, identity, UIDs and issued image', flush=True)
        chart = self.archives['arprt']
        if not self.separate:
            playlist = {'apiVersion': 'arprt.dayflower.github.io/v1alpha1', 'kind': 'ArprtPlaylist',
                        'metadata': {'name': 'schema-upgrade'}, 'spec': {'screens': [{'name': 'http'}], 'optionalUpgradeField': 'accepted'}}
            rejection = self.apply(playlist, check=False)
            require(rejection.returncode != 0 and 'optionalUpgradeField' in rejection.stderr, 'old schema accepted the new optional field')
            chart = upgrade_fixture(self.args.helm, chart, self.directory, self.env)
            self.install(chart, 'upgrade')
            self.apply(playlist)
            require(self.get('arprtplaylist', 'schema-upgrade')['spec'].get('optionalUpgradeField') == 'accepted', 'upgraded schema pruned the field')
            self.assert_state(before)
            print('PASS: parent upgrade accepts the new schema field and preserves existing state', flush=True)
        else:
            require(self.crds() == original_crds, 'controller upgrade modified separately owned CRDs')
        self.helm('uninstall', self.release, '--wait', '--timeout', '120s')
        require(self.snapshot() == before, 'uninstall did not retain CRDs, CRs, PVC, identity or auth')
        if self.separate:
            require(self.crds() == original_crds, 'controller uninstall modified independently owned CRDs')
        # Explicit recovery with the retained claim, external auth and Jobs off.
        # Subsequent same-owner reinstall resumes creation flags and Job wiring.
        self.settings['persistence'] = {'existingClaim': self.claim}
        self.settings['credentials']['authSecrets'][0]['create'] = False
        self.settings['jobs'] = {'enabled': False}
        self.install(chart)
        self.assert_state(before)
        print('PASS: same-owner reinstall restores authenticated issued-image delivery', flush=True)
        self.helm('uninstall', self.release, '--wait', '--timeout', '120s')
        self.settings.pop('persistence')
        self.settings.pop('jobs')
        self.settings['credentials']['authSecrets'][0]['create'] = True
        self.install(chart)
        self.assert_state(before)
        print('PASS: same-owner reinstall restores authenticated issued-image delivery', flush=True)
        self.helm('uninstall', self.release, '--wait', '--timeout', '120s')
        if self.separate:
            require(self.crds() == original_crds, 'controller reinstall modified separately owned CRDs')
            self.helm('uninstall', 'schemas', '--wait', '--timeout', '60s')
            require(self.snapshot() == before, 'independent CRD uninstall did not retain CRDs/resources')
        kubectl('delete', 'namespace', self.namespace, '--wait=true', '--timeout=120s')
        # Intentional schema changes are removed only inside our owned cluster,
        # before another scenario can run. Existing smoke protections stay intact.
        kubectl('delete', 'crd', *smoke.CRDS, '--wait=true', '--timeout=60s')
        print(f'PASS: {self.release}/{self.namespace} lifecycle, delivery, Jobs and permissions', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--helm', required=True)
    parser.add_argument('--image', required=True)
    parser.add_argument('--node-image', required=True)
    parser.add_argument('--load-image', action='store_true')
    sources = parser.add_mutually_exclusive_group(required=True)
    sources.add_argument('--packages')
    sources.add_argument('--oci-repository')
    parser.add_argument('--chart-version')
    args = parser.parse_args()
    def terminate(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, terminate)
    image_values(args.image)
    with tempfile.TemporaryDirectory(prefix='arprt-helm-smoke-') as temporary:
        directory = Path(temporary)
        cluster = 'arprt-helm-' + uuid.uuid4().hex[:8]
        # Isolate Helm registry/repository config and driver as well as kubeconfig.
        env = dict({key: value for key, value in os.environ.items() if not key.startswith('HELM_')}, KUBECONFIG=str(directory / 'kubeconfig'), HELM_DRIVER='secret',
                   HELM_CONFIG_HOME=str(directory / 'helm/config'), HELM_CACHE_HOME=str(directory / 'helm/cache'),
                   HELM_DATA_HOME=str(directory / 'helm/data'), HELM_REGISTRY_CONFIG=str(directory / 'registry.json'))
        for key in list(os.environ):
            if key.startswith('HELM_'):
                del os.environ[key]
        if not args.load_image:
            env['DOCKER_CONFIG'] = str(directory / 'docker')
            Path(env['DOCKER_CONFIG']).mkdir()
        os.environ.update({key: value for key, value in env.items() if key.startswith('HELM_') or key == 'KUBECONFIG'})
        os.environ.pop('HELM_KUBECONTEXT', None)
        archives = packages(args, directory, env)
        try:
            print(run('kind', 'create', 'cluster', '--name', cluster, '--kubeconfig', env['KUBECONFIG'],
                      '--image', args.node_image, '--wait', '90s', env=env).stderr, flush=True)
            if args.load_image:
                run('kind', 'load', 'docker-image', args.image, '--name', cluster, env=env)
            for separate in (False, True):
                Lifecycle(args, directory, archives, env, separate).exercise()
            # Prepare one shared CRD owner before concurrent isolated namespaces.
            # The optional schema fixture has already been removed by Lifecycle.
            run(args.helm, 'install', 'schemas', archives['arprt-crds'], '--namespace', 'smoke-schemas',
                '--create-namespace', '--wait', '--timeout', '60s', env=env)
            print(run('python3', ROOT / 'hack/run-cluster-smoke.py', '--image', args.image,
                      '--helm', args.helm, '--packages', archives['arprt'].parent, '--jobs', '2', env=env).stdout,
                  flush=True)
        except Exception:
            # Resource status only. Avoid logging Secrets, values or MAC-bearing CRs.
            print(kubectl('get', 'pods,jobs,pvc', '-A', check=False).stdout, flush=True)
            raise
        finally:
            result = run('kind', 'delete', 'cluster', '--name', cluster, '--kubeconfig', env['KUBECONFIG'], check=False, env=env)
            require(result.returncode == 0, 'failed to remove disposable Helm cluster: ' + result.stderr)


if __name__ == '__main__':
    main()
