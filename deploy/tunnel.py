#!/usr/bin/env python3
"""Publish the gateway using a dedicated user-owned Cloudflare Tunnel."""
import argparse,json,shutil,subprocess
from pathlib import Path
from urllib.parse import urlsplit
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--overwrite-dns', action='store_true', help='Replace an existing DNS route for the configured hostname')
args=parser.parse_args()
root=Path(__file__).resolve().parents[1];runtime=root/'deploy/runtime';credentials=runtime/'tunnel.json'
env=dict(line.split('=',1) for line in (root/'.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
origin=urlsplit(env.get('PUBLIC_URL',''))
if origin.scheme!='https' or not origin.hostname or origin.port or origin.path not in ('','/') or origin.username or origin.password or origin.query or origin.fragment:
 parser.error('Set PUBLIC_URL to your HTTPS origin without a port or path before creating a tunnel')
hostname=origin.hostname
cloudflared=shutil.which('cloudflared')
if not cloudflared:parser.error('Install cloudflared and authenticate it first')
runtime.mkdir(mode=0o700,parents=True,exist_ok=True)
if not credentials.exists():subprocess.run(['cloudflared','tunnel','create','--credentials-file',str(credentials),'telegram-gateway-pc'],check=True)
data=json.loads(credentials.read_text());tunnel=data['TunnelID'];credentials.chmod(0o600)
config=runtime/'tunnel.yml'
config.write_text(f'''tunnel: {tunnel}
credentials-file: {credentials}
metrics: 127.0.0.1:20246
loglevel: warn
ingress:
  - hostname: {json.dumps(hostname)}
    service: http://127.0.0.1:8086
  - service: http_status:404
''');config.chmod(0o600)
subprocess.run(['cloudflared','tunnel','--config',str(config),'ingress','validate'],check=True)
subprocess.run(['cloudflared','tunnel','route','dns']+(['--overwrite-dns'] if args.overwrite_dns else [])+[tunnel,hostname],check=True)
units=Path.home()/'.config/systemd/user';units.mkdir(parents=True,exist_ok=True)
unit=units/'tgw-tunnel.service'
unit.write_text(f'''[Unit]
Description=Telegram Gateway Cloudflare Tunnel
After=tgw-api.service
Wants=tgw-api.service
[Service]
ExecStart={cloudflared} --no-autoupdate --config {config} tunnel run
Restart=always
RestartSec=5
[Install]
WantedBy=default.target
''')
subprocess.run(['systemctl','--user','daemon-reload'],check=True)
subprocess.run(['systemctl','--user','enable','--now','tgw-tunnel.service'],check=True)
print('Gateway domain routed through its dedicated tunnel.')
