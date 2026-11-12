"""Exercise publication and fail-closed promotion without network or Docker."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

MOCK = r'''#!/usr/bin/env python3
import json, os, pathlib, sys
name=pathlib.Path(sys.argv[0]).name
args=sys.argv[1:]
digest='sha256:'+'a'*64
with open(os.environ['MOCK_LOG'],'a') as log: log.write(name+' '+ ' '.join(args)+'\n')
if name=='docker' and args[:2]==['buildx','build']:
 pathlib.Path(args[args.index('--metadata-file')+1]).write_text(json.dumps({'containerimage.digest':digest}))
elif name=='crane':
 if args[:2]==['auth','login']: sys.stdin.read()
 elif args[0]=='digest':
  if args[1].endswith(':v1.2.3'):
   mode=os.environ['MOCK_MODE']
   if mode=='conflict': print('sha256:'+'b'*64)
   else:
    print('MANIFEST_UNKNOWN: manifest unknown' if mode=='missing' else 'UNAUTHORIZED: registry request failed',file=sys.stderr)
    sys.exit(1)
  else: print(digest)
 elif args[0]=='manifest':
  print(json.dumps({'manifests':[{'platform':{'os':'linux','architecture':a}} for a in ['amd64','arm64']]+[{'annotations':{'vnd.docker.reference.type':'attestation-manifest'}}]}))
 elif args[0]=='tag':
  # Subsequent verification sees the digest promoted by the mock.
  with open(os.environ['MOCK_PROMOTED'],'a') as f: f.write(args[1].split('@')[0]+'\n')
elif name=='cosign' and args[0]=='public-key': print('mock public key')
'''
# Reflect promoted tags without changing the registry error scenarios.
MOCK = MOCK.replace("if args[1].endswith(':v1.2.3'):", "if args[1].endswith(':v1.2.3') and args[1].rsplit(':',1)[0] not in pathlib.Path(os.environ['MOCK_PROMOTED']).read_text().splitlines():")


class RecoveryPublisherTests(unittest.TestCase):
    def run_publisher(self, mode):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo = root / 'repo'
            scripts = repo / 'scripts'
            scripts.mkdir(parents=True)
            shutil.copy(Path(__file__).with_name('publish-recovery-images.sh'), scripts)
            (scripts / 'validate-release-local.sh').write_text('#!/bin/bash\nexit 0\n')
            for args in (['init','-q'], ['add','.'], ['-c','user.name=Test','-c','user.email=test@example.com','commit','-qm','fixture'], ['tag','v1.2.3']):
                subprocess.run(['git',*args],cwd=repo,check=True,capture_output=True)
            bindir = root / 'bin'
            bindir.mkdir()
            for command in ('docker','crane','cosign'):
                p = bindir / command
                p.write_text(MOCK)
                p.chmod(0o700)
            key = root / 'key'
            key.write_text('synthetic test key')
            key.chmod(0o600)
            log = root / 'calls'
            promoted = root / 'promoted'
            promoted.touch()
            env = dict(os.environ, PATH=str(bindir)+os.pathsep+os.environ['PATH'],
                       MOCK_MODE=mode, MOCK_LOG=str(log), MOCK_PROMOTED=str(promoted),
                       RUNNERHUB_RECOVERY_GHCR_USERNAME='test',
                       RUNNERHUB_RECOVERY_GHCR_TOKEN='synthetic-token',
                       RUNNERHUB_RECOVERY_SIGNING_KEY=str(key))
            result = subprocess.run(['bash',str(scripts / 'publish-recovery-images.sh'),
                                     '--version','v1.2.3','--evidence',str(root / 'evidence')],
                                    env=env,text=True,capture_output=True,timeout=30)
            evidence = root / 'evidence/evidence.json'
            return result, log.read_text(), json.loads(evidence.read_text()) if evidence.exists() else None

    def test_missing_version_tags_promote_verified_complete_set(self):
        result, log, evidence = self.run_publisher('missing')
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        self.assertEqual(log.count('crane tag '),6)
        self.assertEqual(len(evidence['images']),6)
        self.assertNotIn('synthetic-token',result.stdout+result.stderr)

    def test_registry_error_cannot_authorize_promotion(self):
        result, log, evidence = self.run_publisher('denied')
        self.assertNotEqual(result.returncode,0)
        self.assertIn('registry lookup failed',result.stdout+result.stderr)
        self.assertNotIn('crane tag ',log)
        self.assertIsNone(evidence)

    def test_conflicting_version_tag_stops_before_any_promotion(self):
        result, log, evidence = self.run_publisher('conflict')
        self.assertNotEqual(result.returncode,0)
        self.assertIn('refusing to overwrite',result.stdout+result.stderr)
        self.assertNotIn('crane tag ',log)
        self.assertIsNone(evidence)
