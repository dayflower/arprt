"""Shared production-chart installation for isolated smoke namespaces.

Existing CRDs are read-only and must match the canonical generated schemas.
Only a wholly absent CRD set may be installed; never adopt an existing owner.
"""
import json
import os
from pathlib import Path
import re
import signal
import subprocess

ROOT = Path(__file__).resolve().parents[1]
CRDS = ['arprtscreens.arprt.dayflower.github.io', 'arprtdevices.arprt.dayflower.github.io',
        'arprtplaylists.arprt.dayflower.github.io']


def kubectl(*args, check=True):
    result = subprocess.run(['kubectl', *map(str, args)], capture_output=True, text=True)
    if check and result.returncode:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result


def image_values(image):
    if '@' in image:
        repository, digest = image.rsplit('@', 1)
        if not repository or not re.fullmatch(r'sha256:[0-9a-f]{64}', digest):
            raise ValueError('image digest must be sha256 with 64 hexadecimal digits')
        return {'repository': repository, 'digest': digest, 'tag': ''}
    if ':' in image.rsplit('/', 1)[-1]:
        repository, tag = image.rsplit(':', 1)
        if repository and tag:
            return {'repository': repository, 'tag': tag, 'digest': ''}
    raise ValueError('smoke image must specify a tag or digest')


def arguments(parser):
    parser.add_argument('--image', required=True)
    parser.add_argument('--helm', required=True)
    parser.add_argument('--packages', required=True, type=Path)
    # Run each suite's finally block when its wrapper is terminated.
    signal.signal(signal.SIGTERM, terminate)


def terminate(*_):
    raise KeyboardInterrupt()


def archive(packages, name):
    matches = [path for path in Path(packages).glob(name + '-*.tgz')
               if re.fullmatch(re.escape(name) + r'-[0-9].*\.tgz', path.name)]
    if len(matches) != 1:
        raise RuntimeError(f'expected exactly one {name} package; clean the output directory')
    return matches[0].resolve()


def environment(directory):
    # Preserve the explicitly selected kubeconfig, isolate all Helm overrides.
    return dict({key: value for key, value in os.environ.items() if not key.startswith('HELM_')},
                HELM_DRIVER='secret', HELM_CONFIG_HOME=str(directory / 'helm/config'),
                HELM_CACHE_HOME=str(directory / 'helm/cache'), HELM_DATA_HOME=str(directory / 'helm/data'),
                HELM_REGISTRY_CONFIG=str(directory / 'registry.json'))


def apply_example(namespace, example):
    directory = ROOT / 'examples/renderers' / example
    if example in ('http/gray-bands', 'http/device-status'):
        name = 'example-renderer' if example == 'http/gray-bands' else 'device-status-renderer'
        kubectl('-n', namespace, 'create', 'configmap', name,
                '--from-file=renderer.py=' + str(directory / 'renderer.py'))
        kubectl('-n', namespace, 'apply', '--validate=strict', '-f', directory / 'renderer.yaml')
    elif example == 'job/gray-bands':
        kubectl('-n', namespace, 'create', 'configmap', 'job-example-image',
                '--from-file=image.png.b64=' + str(directory / 'image.png.b64'))
    filename = 'screens.yaml' if example == 'job/gray-bands' else 'screen.yaml'
    kubectl('-n', namespace, 'apply', '--validate=strict', '-f', directory / filename)


class Installation:
    def __init__(self, args, namespace, directory):
        self.args, self.namespace, self.directory = args, namespace, directory
        self.env = environment(directory)
        self.created_namespace = False
        self.created_crds = {}
        self.crd_release = False
        self.controller_release = False
        self.settings = {'image': image_values(args.image), 'crds': {'enabled': False},
                         'byos': {'publicBaseURL': 'https://byos.example.test/arprt'},
                         'credentials': {'authSecrets': [{'name': 'living-room-auth', 'create': True}],
                                         'identitySecrets': ['living-room-identity']}}

    def helm(self, *args, check=True):
        result = subprocess.run([str(self.args.helm), *map(str, args), '--namespace', self.namespace],
                                capture_output=True, text=True, env=self.env)
        if check and result.returncode:
            raise RuntimeError(result.stderr.strip() or result.stdout.strip())
        return result

    def check_crds(self):
        found = []
        for name in CRDS:
            existing = kubectl('get', 'crd', name, '--ignore-not-found', '-o', 'json').stdout.strip()
            if not existing:
                continue
            plural, group = name.split('.', 1)
            source = ROOT / 'config/crd/bases' / (group + '_' + plural + '.yaml')
            expected = json.loads(kubectl('create', '--dry-run=client', '-f', source, '-o', 'json').stdout)['spec']
            actual = json.loads(existing)['spec']
            # Compare every served/storage version, defaults, validation and
            # subresources, ignoring only API-server defaults outside versions.
            if any(actual.get(key) != expected.get(key) for key in ('group', 'scope', 'names', 'versions')):
                raise RuntimeError('installed CRD contract differs; refusing to modify it')
            found.append(name)
        if found and len(found) != len(CRDS):
            raise RuntimeError('partial CRD installation; refusing to modify it')
        return bool(found)

    def install(self):
        existing = self.check_crds()
        kubectl('create', 'namespace', self.namespace)
        self.created_namespace = True
        if not existing:
            self.crd_release = True
            try:
                self.helm('install', 'schemas', archive(self.args.packages, 'arprt-crds'), '--wait', '--timeout', '60s')
            finally:
                # Also track resources created by an interrupted/failed install.
                for name in CRDS:
                    value = kubectl('get', 'crd', name, '--ignore-not-found', '-o', 'json').stdout.strip()
                    if value:
                        obj = json.loads(value)
                        annotations = obj['metadata'].get('annotations', {})
                        if (annotations.get('meta.helm.sh/release-name') == 'schemas' and
                                annotations.get('meta.helm.sh/release-namespace') == self.namespace):
                            self.created_crds[name] = obj['metadata']['uid']
        for name in CRDS:
            kubectl('wait', '--for=condition=Established', 'crd/' + name, '--timeout=30s')
        self.upgrade(install=True)

    def upgrade(self, install=False):
        values = self.directory / 'controller-values.json'
        values.write_text(json.dumps(self.settings))
        self.controller_release = True
        self.helm('install' if install else 'upgrade', 'arprt', archive(self.args.packages, 'arprt'),
                  '--values', values, '--wait', '--timeout', '180s')

    def cleanup(self):
        # Attempt every cleanup even if an earlier operation fails.
        errors = []
        actions = []
        if self.controller_release:
            actions.append(lambda: self.helm('uninstall', 'arprt', '--ignore-not-found', '--wait', '--timeout', '120s'))
        if self.crd_release:
            actions.append(lambda: self.helm('uninstall', 'schemas', '--ignore-not-found', '--wait', '--timeout', '60s'))
        if self.created_namespace:
            actions.append(lambda: kubectl('delete', 'namespace', self.namespace, '--wait=true', '--timeout=120s'))
        for action in actions:
            try:
                action()
            except Exception as error:
                errors.append(str(error))
        for name, uid in self.created_crds.items():
            try:
                value = kubectl('get', 'crd', name, '--ignore-not-found', '-o', 'json').stdout.strip()
                if value and json.loads(value)['metadata']['uid'] == uid:
                    remaining = json.loads(kubectl('get', name, '--all-namespaces', '-o', 'json').stdout)
                    if not remaining['items']:
                        kubectl('delete', 'crd', name, '--wait=true', '--timeout=30s')
            except Exception as error:
                errors.append(str(error))
        if errors:
            raise RuntimeError('smoke cleanup failed: ' + '; '.join(errors))
