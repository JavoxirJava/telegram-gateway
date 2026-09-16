#!/usr/bin/env python3
"""Start isolated integration-test dependencies, bound only to localhost."""
import json,os,secrets,subprocess
from pathlib import Path
base=Path('/tmp/telegram-gateway-test');base.mkdir(mode=0o700,exist_ok=True)
cfg=base/'environment.json'
if cfg.exists():env=json.loads(cfg.read_text())
else:
 password=secrets.token_urlsafe(30)
 env=dict(POSTGRES_DB='telegram_gateway_test',POSTGRES_USER='test_gateway',POSTGRES_PASSWORD=password,MINIO_ROOT_USER='test-gateway',MINIO_ROOT_PASSWORD=secrets.token_urlsafe(30))
 cfg.write_text(json.dumps(env));cfg.chmod(0o600)
p=base/'infra.env';p.write_text(''.join(f'{k}={v}\n' for k,v in env.items()));p.chmod(0o600)
def run(args):
 r=subprocess.run(args,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
 if r.returncode:raise RuntimeError(r.stderr[:1000])
 return r.stdout
if subprocess.run(['podman','network','exists','tgw-test'],stdout=subprocess.DEVNULL).returncode:run(['podman','network','create','tgw-test'])
services=[
 ('postgres','docker.io/library/postgres:18.6-alpine','15486:5432',[]),
 ('redis','docker.io/library/redis:8.2.9-alpine','16386:6379',[]),
 ('nats','docker.io/library/nats:2.14.6-alpine','14286:4222',['-js']),
 ('minio','quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z','19086:9000',['server','/data'])]
for name,image,port,command in services:
 container='tgw-test-'+name
 if subprocess.run(['podman','container','exists',container],stdout=subprocess.DEVNULL).returncode==0:
  run(['podman','start',container]);print(name,'started',flush=True);continue
 run(['podman','run','-d','--name',container,'--network','tgw-test','--env-file',str(p),'-p','127.0.0.1:'+port,image]+command)
 print(name,'ready to test',flush=True)
