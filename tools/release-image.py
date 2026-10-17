#!/usr/bin/env python3
"""Gate immutable releases and record anonymously verified GHCR image digests."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.parse
import urllib.request

from check_scope import physical_changes

SEMVER = r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?'
LEGACY_SCOPE = 'OG / firmware 1.8.17; DEVELOP physical-device regression'
SCOPE = 'OG corrected gray4 firmware + E1002; DEVELOP physical-device regression'
FIRMWARE_FIELDS = ('Physical-device-og-firmware', 'Physical-device-e1002-firmware')


def field(message, name, default=None):
    values = [line.removeprefix(name + ': ') for line in message.splitlines() if line.startswith(name + ': ')]
    if len(values) > 1:
        raise ValueError('duplicate annotation field: ' + name)
    return values[0] if values else default


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def validate_tag(tag, revision, message, *, allow_legacy_scope=False):
    if not re.fullmatch('v' + SEMVER, tag):
        raise ValueError('release tag must be vMAJOR.MINOR.PATCH with optional SemVer prerelease')
    lines = message.splitlines()
    if lines.count('Physical-device-acceptance: ' + revision) != 1:
        raise ValueError('annotated tag must attest physical acceptance of this exact revision')
    scope = field(message, 'Physical-device-scope')
    if scope != SCOPE and not (allow_legacy_scope and scope == LEGACY_SCOPE):
        raise ValueError('annotated tag must record the required physical-device regression scope')
    if scope == SCOPE:
        for name in FIRMWARE_FIELDS:
            value = field(message, name, '')
            if (not value.strip() or value != value.strip() or len(value) > 256
                    or any(c in value for c in '<>\t\r') or value.lower() in ('unknown', 'pending', 'unavailable')):
                raise ValueError('annotated tag must record exact firmware and distribution: ' + name)
    dates = [line.removeprefix('Physical-device-date: ') for line in lines if line.startswith('Physical-device-date: ')]
    if len(dates) != 1 or not re.fullmatch(r'\d{4}-\d{2}-\d{2}', dates[0]):
        raise ValueError('annotated tag must record one YYYY-MM-DD physical acceptance date (JST)')
    date = datetime.date.fromisoformat(dates[0])
    if date > datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=9))).date():
        raise ValueError('physical acceptance date must not be in the future')
    mode = field(message, 'Physical-device-mode', 'full')
    baseline = field(message, 'Physical-device-baseline')
    if mode not in ('full', 'reuse'):
        raise ValueError('physical acceptance mode must be full or reuse')
    if mode == 'reuse':
        if not baseline or field(message, 'Physical-device-installation') != 'passed':
            raise ValueError('baseline reuse requires its release tag and current-revision installation acceptance')
    elif baseline is not None:
        raise ValueError('full acceptance must not declare baseline reuse')
    return tag[1:]


def physical_scope(baseline, revision):
    if not re.fullmatch('v' + SEMVER, baseline):
        raise ValueError('physical baseline must be a release version tag')
    ref = 'refs/tags/' + baseline
    if run('git', 'cat-file', '-t', ref) != 'tag':
        raise ValueError('physical baseline must be annotated')
    base_revision = run('git', 'rev-parse', ref + '^{commit}')
    message = run('git', 'for-each-ref', '--format=%(contents)', ref)
    validate_tag(baseline, base_revision, message, allow_legacy_scope=True)
    if field(message, 'Physical-device-mode', 'full') != 'full':
        raise ValueError('reuse must refer directly to a full physical acceptance, not another reuse')
    subprocess.run(['git', 'merge-base', '--is-ancestor', base_revision, 'origin/main'], check=True)
    subprocess.run(['git', 'merge-base', '--is-ancestor', base_revision, revision], check=True)
    changed = physical_changes(base_revision, revision)
    scope_changed = field(message, 'Physical-device-scope') != SCOPE
    return {'mode': 'full' if changed or scope_changed else 'reuse', 'baseline': baseline,
            'baselineRevision': base_revision, 'revision': revision,
            'physicalChanges': changed, 'baselineDate': field(message, 'Physical-device-date'),
            'scopeChanged': scope_changed, 'firmware': {name: field(message, name) for name in FIRMWARE_FIELDS}}


def guard(tag):
    ref = 'refs/tags/' + tag
    if run('git', 'cat-file', '-t', ref) != 'tag':
        raise ValueError('release requires an annotated tag with physical-device acceptance')
    revision = run('git', 'rev-parse', ref + '^{commit}')
    if revision != run('git', 'rev-parse', 'HEAD'):
        raise ValueError('checkout must be the exact tagged commit')
    subprocess.run(['git', 'merge-base', '--is-ancestor', revision, 'origin/main'], check=True)
    message = run('git', 'for-each-ref', '--format=%(contents)', ref)
    version = validate_tag(tag, revision, message)
    mode = field(message, 'Physical-device-mode', 'full')
    baseline = field(message, 'Physical-device-baseline', '')
    if mode == 'reuse':
        scope = physical_scope(baseline, revision)
        if scope['mode'] != 'reuse':
            raise ValueError('runtime/unknown changes or a legacy scope require full physical regression: ' + ', '.join(scope['physicalChanges']))
        if any(field(message, name) != scope['firmware'][name] for name in FIRMWARE_FIELDS):
            raise ValueError('firmware/distribution changes require full physical regression')
        if field(message, 'Physical-device-date') < scope['baselineDate']:
            raise ValueError('installation acceptance cannot predate its full baseline')
    outputs = {'tag': tag, 'version': version, 'revision': revision,
               'physical_mode': mode, 'physical_baseline': baseline}
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'], 'a') as stream:
            for key, value in outputs.items():
                stream.write(f'{key}={value}\n')
    print(json.dumps(outputs, indent=2))


def unused(repository, tag):
    owner, package = repository.split('/')
    owner_type = run('gh', 'api', 'repos/' + repository, '--jq', '.owner.type')
    prefix = 'orgs' if owner_type == 'Organization' else 'users'
    endpoint = f'{prefix}/{owner}/packages/container/{package}/versions?per_page=100'
    result = subprocess.run(['gh', 'api', '--paginate', '--slurp', endpoint], capture_output=True, text=True)
    if result.returncode:
        # A missing first package is expected; authentication/network failures fail closed.
        if not re.search(r'\(HTTP 404\)', result.stderr):
            raise RuntimeError(result.stderr)
        return
    reserved = {tag, tag + '-amd64', tag + '-arm64'}
    for page in json.loads(result.stdout):
        for version in page:
            if reserved.intersection(version['metadata']['container']['tags']):
                raise ValueError('release tag or platform tag already exists; never overwrite it')


def validate_index(index):
    manifests = index['manifests']
    platforms = {}
    for manifest in manifests:
        platform = manifest['platform']
        key = platform['os'] + '/' + platform['architecture']
        if key in platforms or key not in ('linux/amd64', 'linux/arm64'):
            raise ValueError('index must contain exactly one amd64 and one arm64 Linux image')
        if platform.get('variant') not in (None, '', 'v8') or (key == 'linux/amd64' and platform.get('variant')):
            raise ValueError('unexpected platform variant')
        platforms[key] = manifest['digest']
    if set(platforms) != {'linux/amd64', 'linux/arm64'}:
        raise ValueError('both release platforms must be present')
    return platforms


def inspect(repository, tag, expected_digest, revision, output):
    # No GitHub token, Docker credential store or caller authentication is used.
    query = urllib.parse.urlencode({'service': 'ghcr.io', 'scope': f'repository:{repository}:pull'})
    with urllib.request.urlopen('https://ghcr.io/token?' + query, timeout=30) as response:
        token = json.load(response)['token']
    accept = ', '.join(['application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json',
                        'application/vnd.oci.image.manifest.v1+json', 'application/vnd.docker.distribution.manifest.v2+json'])

    def fetch(kind, reference):
        request = urllib.request.Request(f'https://ghcr.io/v2/{repository}/{kind}/{reference}',
                                         headers={'Authorization': 'Bearer ' + token, 'Accept': accept})
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read()
        digest = 'sha256:' + hashlib.sha256(body).hexdigest()
        if reference.startswith('sha256:') and reference != digest:
            raise ValueError('registry content does not match requested digest')
        return json.loads(body), digest

    index, digest = fetch('manifests', tag)
    if digest != expected_digest:
        raise ValueError('version tag no longer refers to the published image index digest')
    platforms = validate_index(index)
    for platform, platform_digest in platforms.items():
        manifest, _ = fetch('manifests', platform_digest)
        config, _ = fetch('blobs', manifest['config']['digest'])
        if config['os'] + '/' + config['architecture'] != platform:
            raise ValueError('platform descriptor and image configuration disagree')
        if config['config']['User'] != '1000:1000' or config['config']['Entrypoint'] != ['/arprt']:
            raise ValueError('release runtime must preserve non-root controller entrypoint')
        labels = config['config']['Labels']
        for key, value in {'source': 'https://github.com/' + repository, 'version': tag[1:],
                           'revision': revision, 'licenses': 'MIT',
                           'description': 'ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS. It is not affiliated with or endorsed by TRMNL.'}.items():
            if labels.get('org.opencontainers.image.' + key) != value:
                raise ValueError('incorrect OCI ' + key + ' label')
    evidence = {'image': 'ghcr.io/' + repository, 'tag': tag, 'version': tag[1:], 'revision': revision,
                'indexDigest': digest, 'platformDigests': platforms, 'anonymousRegistryRead': True}
    Path(output).write_text(json.dumps(evidence, indent=2) + '\n')
    print(json.dumps(evidence, indent=2))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    gate = commands.add_parser('guard')
    gate.add_argument('tag')
    free = commands.add_parser('unused')
    free.add_argument('repository')
    free.add_argument('tag')
    physical = commands.add_parser('physical-scope')
    physical.add_argument('baseline', help='annotated release with full physical acceptance')
    check = commands.add_parser('inspect')
    for argument in ['repository', 'tag', 'digest', 'revision', 'output']:
        check.add_argument(argument)
    args = parser.parse_args()
    if args.command == 'guard':
        guard(args.tag)
    elif args.command == 'physical-scope':
        print(json.dumps(physical_scope(args.baseline, run('git', 'rev-parse', 'HEAD')), indent=2))
    elif args.command == 'unused':
        unused(args.repository, args.tag)
    else:
        inspect(args.repository, args.tag, args.digest, args.revision, args.output)


if __name__ == '__main__':
    main()
