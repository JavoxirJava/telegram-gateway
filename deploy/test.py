#!/usr/bin/env python3
import json,os,subprocess,urllib.parse,shutil
from pathlib import Path
cfg=json.loads(Path('/tmp/telegram-gateway-test/environment.json').read_text())
env=dict(os.environ)
env['TEST_DATABASE_URL']='postgres://'+urllib.parse.quote(cfg['POSTGRES_USER'])+':'+urllib.parse.quote(cfg['POSTGRES_PASSWORD'])+'@127.0.0.1:15486/'+cfg['POSTGRES_DB']+'?sslmode=disable'
env['TEST_MINIO_USER']=cfg['MINIO_ROOT_USER'];env['TEST_MINIO_PASSWORD']=cfg['MINIO_ROOT_PASSWORD']
go=os.getenv('GO') or shutil.which('go')
if not go:raise SystemExit('Go is required. Install the version in go.mod or set GO=/path/to/go.')
raise SystemExit(subprocess.call([go,'test','-race','-count=1','./...'],env=env))
