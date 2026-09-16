#!/usr/bin/env python3
"""Back up the database and encryption/configuration keys to a private folder."""
import datetime,os,shutil,subprocess
from pathlib import Path
root=Path(__file__).resolve().parents[1]
backup=root/'data/backups'/datetime.datetime.now().strftime('%Y%m%d-%H%M%S')
backup.mkdir(mode=0o700,parents=True)
with (backup/'postgres.dump').open('wb') as f:
 subprocess.run(['podman','exec','tgw-postgres','pg_dump','-U','gateway_owner','-d','telegram_gateway','-Fc'],stdout=f,check=True)
shutil.copy2(root/'.env',backup/'.env')
shutil.copytree(root/'deploy/runtime',backup/'runtime')
for p in backup.rglob('*'):
 p.chmod(0o700 if p.is_dir() else 0o600)
print(backup)
print('Database and configuration keys backed up. MinIO and TDLib data remain in their named volumes; this is not a media/session-volume backup.')
