#!/usr/bin/env python3
"""Install and start this project's rootless Podman Quadlets. Preserve volumes."""
import os,subprocess,secrets,time
from pathlib import Path
root=Path(__file__).resolve().parents[1]
runtime=root/'deploy/runtime';runtime.mkdir(mode=0o700,parents=True,exist_ok=True);runtime.chmod(0o700)
env={}
for line in (root/'.env').read_text().splitlines():
 if '=' in line and not line.lstrip().startswith('#'):
  k,_,v=line.partition('=');env[k.strip()]=v.strip()
def file(path,text,mode=0o600):
 path.write_text(text);path.chmod(mode)
def run(args,input=None):
 result=subprocess.run(args,input=input,text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
 if result.returncode:raise RuntimeError('Command failed: '+args[0]+' '+result.stderr[:2000])
 return result.stdout.strip()
ownerfile=runtime/'postgres.env'
if ownerfile.exists():ownerpass=dict(line.split('=',1) for line in ownerfile.read_text().splitlines())['POSTGRES_PASSWORD']
else:ownerpass=secrets.token_urlsafe(36)
file(ownerfile,f"POSTGRES_DB={env['POSTGRES_DB']}\nPOSTGRES_USER=gateway_owner\nPOSTGRES_PASSWORD={ownerpass}\n")
migrate=dict(env,POSTGRES_USER='gateway_owner',POSTGRES_PASSWORD=ownerpass)
file(runtime/'migrate.env',''.join(f'{k}={v}\n' for k,v in migrate.items()))
file(runtime/'minio.env',f"MINIO_ROOT_USER={env['MINIO_ACCESS_KEY']}\nMINIO_ROOT_PASSWORD={env['MINIO_SECRET_KEY']}\n")
file(runtime/'redis.conf',f"bind 0.0.0.0\nport 6379\nappendonly yes\ndir /data\nprotected-mode yes\nrequirepass {env['REDIS_PASSWORD']}\nmaxmemory 256mb\nmaxmemory-policy noeviction\n",0o644)
units=Path.home()/'.config/containers/systemd';units.mkdir(parents=True,exist_ok=True)
file(units/'tgw.network','[Network]\nNetworkName=tgw\n',0o644)
for name in ['postgres','redis','nats','minio','tdlib']:
 file(units/f'tgw-{name}.volume',f'[Volume]\nVolumeName=tgw-{name}\n',0o644)
services={
 'postgres':('docker.io/library/postgres:18.6-alpine',f'EnvironmentFile={ownerfile}\nVolume=tgw-postgres.volume:/var/lib/postgresql\nHealthCmd=pg_isready -U gateway_owner -d telegram_gateway\n'),
 'redis':('docker.io/library/redis:8.2.9-alpine',f'Volume=tgw-redis.volume:/data\nVolume={runtime}/redis.conf:/usr/local/etc/redis/redis.conf:ro\nExec=redis-server /usr/local/etc/redis/redis.conf\n'),
 'nats':('docker.io/library/nats:2.14.6-alpine','Volume=tgw-nats.volume:/data\nExec=-js -sd /data -m 8222\n'),
 'minio':('quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z',f'Volume=tgw-minio.volume:/data\nEnvironmentFile={runtime}/minio.env\nExec=server /data --console-address :9001\n'),
}
for name,(image,extra) in services.items():
 file(units/f'tgw-{name}.container',f'[Unit]\nDescription=Telegram Gateway {name}\n[Container]\nImage={image}\nContainerName=tgw-{name}\nNetwork=tgw.network\n{extra}[Service]\nRestart=always\nTimeoutStartSec=180\n[Install]\nWantedBy=default.target\n',0o644)
file(units/'tgw-api.container',f'''[Unit]
Description=Telegram Gateway API, MCP and Telegram sync
Wants=tgw-postgres.service tgw-redis.service tgw-nats.service tgw-minio.service
After=tgw-postgres.service tgw-redis.service tgw-nats.service tgw-minio.service
[Container]
Image=localhost/telegram-gateway:local
ContainerName=tgw-api
Network=tgw.network
EnvironmentFile={root}/.env
PublishPort=127.0.0.1:8086:8080
Volume=tgw-tdlib.volume:/data:U
HealthCmd=/usr/local/bin/gateway-health
HealthInterval=30s
HealthTimeout=10s
HealthRetries=3
HealthStartPeriod=30s
HealthOnFailure=kill
[Service]
Restart=always
TimeoutStartSec=180
TimeoutStopSec=35
[Install]
WantedBy=default.target
''',0o644)
run(['systemctl','--user','daemon-reload'])
for name in services:
 run(['systemctl','--user','start',f'tgw-{name}.service']);print(name,'service started',flush=True)
for attempt in range(60):
 if subprocess.run(['podman','exec','tgw-postgres','pg_isready','-U','gateway_owner','-d',env['POSTGRES_DB']],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:break
 time.sleep(1)
else:raise RuntimeError('PostgreSQL startup timed out')
run(['podman','run','--rm','--network','tgw','--env-file',str(runtime/'migrate.env'),'--entrypoint','/usr/local/bin/gateway-migrate','localhost/telegram-gateway:local'])
# Runtime role can change application data but cannot alter tables or audit rows.
def literal(v):return "'"+v.replace("'","''")+"'"
password=literal(env['POSTGRES_PASSWORD'])
sql=f'''DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='telegram_gateway') THEN CREATE ROLE telegram_gateway LOGIN; END IF; END $$;
ALTER ROLE telegram_gateway PASSWORD {password};
GRANT CONNECT ON DATABASE telegram_gateway TO telegram_gateway;
GRANT USAGE ON SCHEMA public TO telegram_gateway;
GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO telegram_gateway;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO telegram_gateway;
REVOKE INSERT,UPDATE,DELETE ON schema_migrations FROM telegram_gateway;
REVOKE UPDATE,DELETE ON audit_logs FROM telegram_gateway;
'''
run(['podman','exec','-i','tgw-postgres','psql','-v','ON_ERROR_STOP=1','-U','gateway_owner','-d',env['POSTGRES_DB']],input=sql)
run(['systemctl','--user','restart','tgw-api.service']);print('API service started',flush=True)
linger=run(['loginctl','show-user',str(os.getuid()),'-p','Linger','--value'])
if linger!='yes':run(['loginctl','enable-linger',str(os.getuid())])
print('Rootless Podman deployment installed; persistent volumes retained.',flush=True)
