#!/usr/bin/env python3
"""Validate mixed playlists and automatic discovery in an isolated namespace.

Reuses identical installed CRD schemas without modifying them; removes only
resources this run created. Requires kubectl and a cluster-accessible image.
"""
import argparse
import overlay_checks
import helm_install
import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import time
import urllib.request
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
    namespace = 'arprt-playlist-' + uuid.uuid4().hex[:8]
    with tempfile.TemporaryDirectory(prefix='arprt-playlist-smoke-') as temporary:
        directory = Path(temporary)
        installation = helm_install.Installation(args, namespace, directory)
        try:
            installation.settings['jobs'] = {'deadlineSeconds': 30}
            installation.install()

            def apply(obj):
                path = directory / 'resource.json'
                path.write_text(json.dumps(obj))
                kubectl('-n', namespace, 'apply', '--validate=strict', '-f', str(path))
                path.unlink()

            kubectl('-n', namespace, 'rollout', 'status', 'deployment/arprt-arprt', '--timeout=180s')
            image = base64.b64encode((ROOT / 'testdata/png/target-gray4.png').read_bytes()).decode()
            # The renderer implements both HTTP transports without Kubernetes access.
            program = '''import base64,json,os
from http.server import BaseHTTPRequestHandler,HTTPServer
class Handler(BaseHTTPRequestHandler):
 def handle_render(self):
  body=self.rfile.read(int(self.headers.get('Content-Length','0')))
  if self.command=='POST':
   context=json.loads(body)
   assert context['device']['battery']['voltage']==3.91
   assert 'apiKey' not in body.decode() and 'hardwareID' not in body.decode()
  else: assert not body
  data=base64.b64decode(os.environ['IMAGE_PNG'])
  self.send_response(200);self.send_header('Content-Type','image/png');self.end_headers();self.wfile.write(data)
 def do_GET(self): self.handle_render()
 def do_POST(self): self.handle_render()
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8080),Handler).serve_forever()
'''
            apply({'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'input'},'data':{'IMAGE_PNG':image,'server.py':program}})
            apply({'apiVersion':'apps/v1','kind':'Deployment','metadata':{'name':'renderer'},'spec':{'replicas':1,
                'selector':{'matchLabels':{'app':'renderer'}},'template':{'metadata':{'labels':{'app':'renderer'}},'spec':{
                    'automountServiceAccountToken':False,'containers':[{'name':'renderer','image':'python:3.13-alpine',
                        'command':['python','/input/server.py'],'envFrom':[{'configMapRef':{'name':'input'}}],
                        'volumeMounts':[{'name':'input','mountPath':'/input','readOnly':True}]}],
                    'volumes':[{'name':'input','configMap':{'name':'input'}}]}}}})
            apply({'apiVersion':'v1','kind':'Service','metadata':{'name':'renderer'},'spec':{'selector':{'app':'renderer'},'ports':[{'port':8080}]}})
            kubectl('-n',namespace,'rollout','status','deployment/renderer','--timeout=180s')
            sources = {
                'a-get': {'http':{'url':'http://renderer:8080/render','method':'GET'}},
                'b-post': {'http':{'url':'http://renderer:8080/render','method':'POST'}},
                'c-job': {'job':{'renderer':{'image':'busybox:1.37.0','command':['sh','-ec'],
                    'args':['test -r /arprt/context.json; test ! -e /var/run/secrets/kubernetes.io/serviceaccount/token; printf %s "$IMAGE_PNG" | base64 -d > /arprt/output.png'],
                    'envFrom':[{'configMapRef':{'name':'input'}}]}}}}
            uids = {}
            for name, source in sources.items():
                apply({'apiVersion':'arprt.dayflower.github.io/v1alpha1','kind':'ArprtScreen','metadata':{'name':name},'spec':{'source':source,'overlay':{'enabled':True,'cycleCorners':True,'margin':{'horizontal':12,'vertical':4}}}})
                uids[name] = json.loads(kubectl('-n',namespace,'get','arprtscreen',name,'-o','json').stdout)['metadata']['uid']
            refs = [{'name':'b-post'},{'name':'missing'},{'name':'c-job'},{'name':'a-get'}]
            apply({'apiVersion':'arprt.dayflower.github.io/v1alpha1','kind':'ArprtPlaylist','metadata':{'name':'cycle'},'spec':{'screens':refs}})
            apply({'apiVersion':'arprt.dayflower.github.io/v1alpha1','kind':'ArprtDevice','metadata':{'name':'living-room'},
                'spec':{'authSecretRef':{'name':'living-room-auth'},'playlistRef':{'name':'cycle'}}})
            token = ''
            headers = {'ID':'02:00:00:00:00:01','Model':'og','FW-Version':'1.8.17','Width':'800','Height':'480','Battery-Voltage':'3.91','RSSI':'-48'}
            path = '/arprt/devices/' + namespace + '/living-room'

            def fetch(base, endpoint):
                request = urllib.request.Request(base + endpoint, headers={**headers,'Access-Token':token})
                with urllib.request.urlopen(request,timeout=15) as response:
                    return response.read()

            def device():
                return json.loads(kubectl('-n',namespace,'get','arprtdevice','living-room','-o','json').stdout)

            def expire():
                kubectl('-n',namespace,'patch','arprtdevice','living-room','--subresource=status','--type=merge','-p',
                    json.dumps({'status':{'screenCycle':{'servedAt':'2000-01-01T00:00:00.000000Z'}}}))

            def display(base):
                return json.loads(fetch(base,path+'/api/display'))

            last_turn, corner_index = None, 0

            def turn(base, name):
                nonlocal last_turn, corner_index
                response = display(base)
                if response['status'] != 0 or device()['status']['screenCycle']['screenUID'] != uids[name]:
                    raise RuntimeError('wrong screen-cycle turn: ' + name)
                image_path = response['image_url'].removeprefix('https://byos.example.test')
                committed = device()['status']['screenCycle']
                if committed != last_turn:
                    corner = overlay_checks.CORNERS[corner_index % 4]
                    overlay_checks.assert_overlay(base64.b64decode(image), fetch(base,image_path), corner, margin_horizontal=12, margin_vertical=4)
                    corner_index += 1
                    last_turn = committed
                replay_bytes = fetch(base,image_path)
                headers['Percent-Charged'], headers['RSSI'] = '0', '-99'
                headers['Battery-Charging'] = '1'
                if display(base) != response or fetch(base,image_path) != replay_bytes:
                    raise RuntimeError('retry telemetry changed the committed overlay')
                headers.pop('Percent-Charged')
                headers.pop('Battery-Charging')
                headers['RSSI'] = '-48'
                return response

            with forwarded(namespace,8080,directory) as base:
                token = json.loads(fetch(base,path+'/api/setup'))['api_key']
                first = turn(base,'b-post')
                # Job preparation must happen before c-job is ever selected.
                def job_ready():
                    jobs = json.loads(kubectl('-n',namespace,'get','jobs','-o','json').stdout)['items']
                    return bool(jobs) and any(any(c['type']=='Complete' and c['status']=='True' for c in j.get('status',{}).get('conditions',[])) for j in jobs)
                wait(job_ready,120)
            kubectl('-n',namespace,'rollout','restart','deployment/arprt-arprt')
            kubectl('-n',namespace,'rollout','status','deployment/arprt-arprt','--timeout=180s')
            with forwarded(namespace,8080,directory) as base:
                if display(base) != first:
                    raise RuntimeError('screen-cycle replay lost across restart')
                expire(); turn(base,'c-job'); expire(); turn(base,'a-get'); expire(); turn(base,'b-post')
                # Editing the playlist resets its order, including within a retry window.
                kubectl('-n',namespace,'patch','arprtplaylist','cycle','--type=merge','-p',json.dumps({'spec':{'screens':[{'name':'a-get'},{'name':'c-job'},{'name':'b-post'}]}}))
                turn(base,'a-get')
                # Discovery reuses the same pairs, in deterministic name order.
                kubectl('-n',namespace,'patch','arprtdevice','living-room','--type=merge','-p',json.dumps({'spec':{'playlistRef':None}}))
                turn(base,'a-get'); expire(); turn(base,'b-post'); expire(); turn(base,'c-job')
                kubectl('-n',namespace,'delete','arprtscreen','a-get')
                turn(base,'b-post')
                # Removing Job eligibility cancels its scheduling state.
                kubectl('-n',namespace,'delete','arprtscreen','c-job')
                before = {j['metadata']['name'] for j in json.loads(kubectl('-n',namespace,'get','jobs','-o','json').stdout)['items']}
                time.sleep(3)
                after = {j['metadata']['name'] for j in json.loads(kubectl('-n',namespace,'get','jobs','-o','json').stdout)['items']}
                if not after.issubset(before): raise RuntimeError('removed Job pair scheduled new work')
                kubectl('-n',namespace,'delete','arprtscreen','b-post')
                if display(base) != {'status':202}:
                    raise RuntimeError('empty discovery did not return pending')
            account = 'system:serviceaccount:' + namespace + ':arprt-arprt'
            for ns, allowed in [(namespace,True),('default',False)]:
                result = kubectl('auth','can-i','list','arprtplaylists','-n',ns,'--as='+account,check=False)
                if (result.stdout.strip()=='yes') != allowed:
                    raise RuntimeError('playlist RBAC namespace mismatch')
            print('PASS: mixed GET/POST/Job playlists and discovery, telemetry, Job pre-rendering, retry/restart persistence, shared overlay corners/replay, edits, deletion, empty set and namespace RBAC')
        finally:
            installation.cleanup()
            print('Disposable Helm test resources removed; existing CRDs untouched')


if __name__ == '__main__':
    main()
