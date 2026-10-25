// Runs only against the disposable Compose contour after API/UI tests.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
const directory = process.env.POAKTU_REFERENCE_DIR;
assert(directory, 'POAKTU_REFERENCE_DIR is required');
function docker(args) {
  const r = spawnSync('docker', args, { encoding: 'utf8', timeout: 180000 });
  assert.ifError(r.error); assert.equal(r.status, 0, r.stderr);
  return r.stdout;
}
function cli(args, expected, status) {
  const r = spawnSync('docker', ['compose', '-f', 'compose.yaml', '-f', 'compose.integrity.yaml', 'run', '--rm', '--no-deps', 'verifier', ...args], { encoding: 'utf8', timeout: 180000 });
  assert.ifError(r.error); assert.equal(r.status, expected, r.stderr + r.stdout);
  const report = JSON.parse(r.stdout.trim()); assert.equal(report.status, status);
  console.log(`${status} (exit ${expected})`); return report;
}
const checkpoint = '/reference/checkpoint-001.json', publicKey = '/reference/trusted-public.pem';
const privateKey = '/reference/signer.pem';
try {
  // All ordinary background jobs must drain before freezing this test contour.
  for (let i = 0; ; i++) {
    const pending = docker(['compose', 'exec', '-T', 'db', 'psql', '-U', 'poaktu_owner', '-d', 'poaktu', '-Atc', "SELECT (SELECT count(*) FROM outbox WHERE delivered_at IS NULL)+(SELECT count(*) FROM event_inbox WHERE status='PENDING')+(SELECT count(*) FROM edo_deliveries WHERE completed_at IS NULL)+(SELECT count(*) FROM edo_outbox e WHERE NOT EXISTS(SELECT 1 FROM edo_deliveries d WHERE d.automatic_basis=e.event_id))"]).trim();
    if (pending === '0') break;
    assert(i < 60, 'Background work did not drain; no new reference may be trusted');
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  docker(['compose', 'stop', 'api']);
  cli(['keygen', '--private-key', privateKey, '--public-key', publicKey], 0, 'KEYS_CREATED');
  const first = cli(['checkpoint', '--private-key', privateKey, '--out', checkpoint, '--acknowledge-current-state'], 0, 'CHECKPOINT_CREATED');
  const args = ['verify', '--checkpoint', checkpoint, '--public-key', publicKey, '--checkpoint-sha256', first.checkpoint_sha256];
  cli(args, 0, 'MATCH');
  cli(['verify', '--checkpoint', checkpoint, '--public-key', publicKey], 3, 'UNVERIFIABLE');
  cli([...args.slice(0, -1), '0'.repeat(64)], 3, 'UNVERIFIABLE');
  cli(['checkpoint', '--private-key', privateKey, '--out', checkpoint, '--acknowledge-current-state'], 3, 'UNVERIFIABLE');
  cli(args, 0, 'MATCH'); // Existing reference was not replaced.
  const next = cli(['checkpoint', '--private-key', privateKey, '--out', '/reference/checkpoint-002.json', '--acknowledge-current-state'], 0, 'CHECKPOINT_CREATED');
  assert.notEqual(first.checkpoint_sha256, next.checkpoint_sha256);
  cli([...args.slice(0, -1), next.checkpoint_sha256], 3, 'UNVERIFIABLE');
  cli(['keygen', '--private-key', '/reference/other-signer.pem', '--public-key', '/reference/other-public.pem'], 0, 'KEYS_CREATED');
  cli(['verify', '--checkpoint', checkpoint, '--public-key', '/reference/other-public.pem', '--checkpoint-sha256', first.checkpoint_sha256], 3, 'UNVERIFIABLE');
  docker(['compose', 'up', '-d', '--wait', '--wait-timeout', '90', 'api']);
  cli(args, 3, 'QUIESCENCE_REQUIRED');
  docker(['compose', 'stop', 'api']);
  cli(args, 0, 'MATCH');
  console.log('Independent integrity CLI smoke passed; keys and references are not uploaded.');
} finally {
  docker(['compose', 'up', '-d', '--wait', '--wait-timeout', '90', 'api']);
}
