"""Protect read-only schema reuse and cleanup across chart-install failures."""
import argparse
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import helm_install as install


class InstallationTests(unittest.TestCase):
    def installation(self, directory):
        return install.Installation(argparse.Namespace(image='arprt:test', helm='/tools/helm', packages='/packages'),
                                    'isolated-smoke', Path(directory))

    def test_existing_schemas_are_read_only_and_must_match_every_version(self):
        expected = {'group': 'arprt.dayflower.github.io', 'scope': 'Namespaced', 'names': {'kind': 'ArprtDevice'},
                    'versions': [{'name': 'v1alpha1', 'served': True, 'storage': True,
                                  'schema': {'type': 'object'}, 'subresources': {'status': {}}}]}
        for change in ('none', 'schema', 'subresources', 'version', 'partial'):
            actual = copy.deepcopy(expected)
            if change == 'schema':
                actual['versions'][0]['schema']['description'] = 'changed'
            elif change == 'subresources':
                actual['versions'][0]['subresources'] = {}
            elif change == 'version':
                actual['versions'].append(dict(actual['versions'][0], name='v1beta1'))
            calls = []

            def kubectl(*args, **kwargs):
                calls.append(args)
                if args[:2] == ('get', 'crd'):
                    value = '' if change == 'partial' and args[2] == install.CRDS[-1] else json.dumps({'spec': actual})
                else:
                    self.assertEqual(args[:2], ('create', '--dry-run=client'))
                    value = json.dumps({'spec': expected})
                return subprocess.CompletedProcess(args, 0, value, '')

            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory, \
                    patch.object(install, 'kubectl', side_effect=kubectl):
                instance = self.installation(directory)
                if change == 'none':
                    self.assertTrue(instance.check_crds())
                else:
                    with self.assertRaisesRegex(RuntimeError, 'refusing to modify'):
                        instance.install()
                self.assertFalse(instance.created_namespace)
                self.assertTrue(all(args[0] == 'get' or '--dry-run=client' in args for args in calls))

    def test_partial_install_failure_cleans_owned_resources_but_preserves_replaced_crds(self):
        with tempfile.TemporaryDirectory() as directory:
            instance = self.installation(directory)
            instance.created_namespace = instance.crd_release = instance.controller_release = True
            instance.created_crds = {install.CRDS[0]: 'owned', install.CRDS[1]: 'previous-owner'}
            calls = []

            def kubectl(*args, **kwargs):
                calls.append(args)
                if args[:2] == ('get', 'crd'):
                    value = {'metadata': {'uid': 'owned' if args[2] == install.CRDS[0] else 'replacement'}}
                else:
                    value = {'items': []}
                return subprocess.CompletedProcess(args, 0, json.dumps(value), '')

            with patch.object(instance, 'helm', side_effect=RuntimeError('injected uninstall failure')), \
                    patch.object(install, 'kubectl', side_effect=kubectl):
                with self.assertRaisesRegex(RuntimeError, 'cleanup failed'):
                    instance.cleanup()
            self.assertIn(('delete', 'namespace', instance.namespace, '--wait=true', '--timeout=120s'), calls)
            self.assertIn(('delete', 'crd', install.CRDS[0], '--wait=true', '--timeout=30s'), calls)
            self.assertFalse(any(args[:3] == ('delete', 'crd', install.CRDS[1]) for args in calls))

    def test_failed_crd_install_tracks_and_removes_only_its_partial_output(self):
        with tempfile.TemporaryDirectory() as directory:
            instance = self.installation(directory)
            live, calls = {}, []

            def kubectl(*args, **kwargs):
                calls.append(args)
                if args[:2] == ('get', 'crd'):
                    value = json.dumps(live[args[2]]) if args[2] in live else ''
                else:
                    value = json.dumps({'items': []})
                return subprocess.CompletedProcess(args, 0, value, '')

            def helm(*args, **kwargs):
                if args[0] == 'install':
                    live[install.CRDS[0]] = {'metadata': {'uid': 'partial', 'annotations': {
                        'meta.helm.sh/release-name': 'schemas', 'meta.helm.sh/release-namespace': instance.namespace}}}
                    raise RuntimeError('injected CRD install failure')
                return subprocess.CompletedProcess(args, 0, '', '')

            with patch.object(install, 'kubectl', side_effect=kubectl), \
                    patch.object(install, 'archive', return_value=Path('/packages/crds.tgz')), \
                    patch.object(instance, 'helm', side_effect=helm):
                try:
                    with self.assertRaisesRegex(RuntimeError, 'injected CRD install failure'):
                        instance.install()
                finally:
                    instance.cleanup()
            self.assertEqual(instance.created_crds, {install.CRDS[0]: 'partial'})
            self.assertIn(('delete', 'crd', install.CRDS[0], '--wait=true', '--timeout=30s'), calls)
            self.assertFalse(instance.controller_release)


if __name__ == '__main__':
    unittest.main()
