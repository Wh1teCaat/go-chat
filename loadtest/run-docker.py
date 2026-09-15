#!/usr/bin/env python3
"""Run the built load generator against the isolated loadtest Compose project."""
import argparse
import datetime
import json
import pathlib
import subprocess
import time

parser = argparse.ArgumentParser()
parser.add_argument('--binary', default='/tmp/gochat-loadtest')
parser.add_argument('--output', default='loadtest/results/docker-' + datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
args = parser.parse_args()
out = pathlib.Path(args.output)
out.mkdir(parents=True, exist_ok=False)
compose = ['docker', 'compose', '-p', 'gochat-bench', '-f', 'loadtest/compose.yml']

def capture(cmd):
    return subprocess.check_output(cmd, text=True, timeout=30).strip()

ids = capture(compose + ['ps', '-q']).splitlines()
if len(ids) != 4:
    raise SystemExit('Expected four running dedicated benchmark containers')
meta = {
    'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
    'docker': json.loads(capture(['docker', 'info', '--format', '{"version":"{{.ServerVersion}}","cpus":{{.NCPU}},"memory_bytes":{{.MemTotal}},"os":"{{.OperatingSystem}}"}'])),
    'containers': json.loads(capture(['docker', 'inspect'] + ids)),
    'redis_subscribers': capture(compose + ['exec', '-T', 'redis', 'redis-cli', 'PUBSUB', 'NUMSUB', 'ws:events']),
    'git_revision': capture(['git', 'rev-parse', 'HEAD']),
    'dirty_paths': capture(['git', 'diff', '--name-only']),
    'note': 'Direct published backend ports; one endpoint per backend; shared Redis and PostgreSQL; test-only HTTP rate limit disabled; no nginx; existing unrelated containers left running.',
}
# Keep reproducibility fields, without serializing full container environment variables.
meta['containers'] = [{
    'name': c['Name'], 'image': c['Image'], 'started': c['State']['StartedAt'],
    'nano_cpus': c['HostConfig']['NanoCpus'], 'memory_limit': c['HostConfig']['Memory'],
    'ports': c['NetworkSettings']['Ports']
} for c in meta['containers']]
(out/'environment.json').write_text(json.dumps(meta, indent=2)+'\n')
# First compare with the native-process run, then separate connection count from message rate.
stages = [(f'{n}-connections-10s', n//2, 200, 20) for n in [20,100,200,500,1000]]
stages += [('100-connections-60s',50,1200,20), ('1000-connections-1msg-60s',500,60,1)]
for name,pairs,messages,rate in stages:
    print('START '+name, flush=True)
    log = out/(name+'.log')
    stats = []
    cmd = [args.binary, '-urls', 'http://127.0.0.1:18081,http://127.0.0.1:18082',
           '-pairs',str(pairs),'-messages',str(messages),'-rate',str(rate),'-drain','5s','-output',str(out/(name+'.json'))]
    with log.open('w') as stream:
        proc = subprocess.Popen(cmd, stdout=stream, stderr=stream)
        try:
            while proc.poll() is None:
                try:
                    raw = capture(['docker','stats','--no-stream','--format','{{json .}}']+ids)
                    stats.append({'utc':datetime.datetime.now(datetime.timezone.utc).isoformat(),
                                  'phase':'measurement' if 'load:' in log.read_text() else 'setup',
                                  'containers':[json.loads(x) for x in raw.splitlines()]})
                except (subprocess.SubprocessError, ValueError) as exc:
                    stats.append({'error':str(exc)})
                time.sleep(2)
        finally:
            if proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=20)
    (out/(name+'-resources.json')).write_text(json.dumps({'exit_code':proc.returncode,'samples':stats},indent=2)+'\n')
    print('DONE '+name+' exit='+str(proc.returncode),flush=True)
    if proc.returncode == 2:
        print(log.read_text()[-3000:],flush=True)
        raise SystemExit(2)
    # Allow server processing from a saturated stage to stop before the next setup.
    time.sleep(5)
print('RESULTS '+str(out),flush=True)
