#!/usr/bin/env python3
"""Create/update local runtime configuration without printing secret values."""
import argparse, base64, os, secrets
from pathlib import Path
from urllib.parse import urlsplit
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--public-url', help='Gateway origin; defaults to existing PUBLIC_URL or http://127.0.0.1:8086')
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
p=root/'.env'
existing={}
if p.exists():
 for line in p.read_text().splitlines():
  if '=' in line and not line.lstrip().startswith('#'):
   k,_,v=line.partition('=');existing[k.strip()]=v.strip()
def credential(key,default_bad=()):
 old=existing.get(key,'')
 return old if old and old not in default_bad else secrets.token_urlsafe(36)
values=dict(existing)
public_url=(args.public_url or existing.get('PUBLIC_URL') or 'http://127.0.0.1:8086').rstrip('/')
origin=urlsplit(public_url)
if origin.scheme not in ('http','https') or not origin.hostname or origin.username or origin.password or origin.path or origin.query or origin.fragment or any(c.isspace() for c in public_url):
 parser.error('--public-url must be an HTTP(S) origin without credentials, path, query or fragment')
if origin.scheme=='http' and origin.hostname not in ('localhost','127.0.0.1','::1'):
 parser.error('Use HTTPS for a non-loopback public URL')
values.update(APP_ENV='production',HTTP_ADDR=':8080',PUBLIC_URL=public_url,SHUTDOWN_TIMEOUT='20s',POSTGRES_HOST='tgw-postgres',POSTGRES_PORT='5432',POSTGRES_DB='telegram_gateway',POSTGRES_USER='telegram_gateway',POSTGRES_SSLMODE='disable',REDIS_ADDR='tgw-redis:6379',REDIS_DB='0',NATS_URL='nats://tgw-nats:4222',MINIO_ENDPOINT='tgw-minio:9000',MINIO_ACCESS_KEY='telegram-gateway',MINIO_USE_SSL='false',MINIO_BUCKET='telegram-media',TDLIB_DATA_DIR='/data/tdlib')
for key,bad in [('POSTGRES_PASSWORD',('change-me',)),('REDIS_PASSWORD',()),('MINIO_SECRET_KEY',('change-me-minio-secret',)),('GATEWAY_ADMIN_TOKEN',())]:values[key]=credential(key,bad)
values['TELEGRAM_SESSION_KEY']=existing.get('TELEGRAM_SESSION_KEY') or base64.b64encode(secrets.token_bytes(32)).decode()
values.setdefault('TELEGRAM_API_ID','');values.setdefault('TELEGRAM_API_HASH','')
# Preserve the Telegram values the owner supplied; no secret is printed.
fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f:
 for k,v in values.items():f.write(f'{k}={v}\n')
p.chmod(0o600)
print('Local configuration ready; Telegram credentials preserved.')
