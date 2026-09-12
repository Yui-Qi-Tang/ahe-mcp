import assert from 'node:assert/strict';
import test from 'node:test';
import { containsMachinePath, publicFailureLabel } from './check-public-paths.mjs';

test('detects home and runtime paths without printing them', () => {
  const home = ['', 'Users', 'fixture-account', 'work', 'README.md'].join('/');
  const unix = ['', 'home', 'fixture-account', 'work'].join('/');
  const runtime = ['', 'private', 'var', 'folders', 'ab', 'fixture-runtime', 'T'].join('/');
  const windows = ['C:', 'Users', 'fixture-account', 'work'].join('\\');
  for (const path of [home, unix, runtime, windows]) {
    assert.equal(containsMachinePath(path), true);
    assert.equal(containsMachinePath(JSON.stringify(path)), true);
    assert.equal(containsMachinePath(encodeURIComponent(path)), true);
    assert.equal(containsMachinePath(path.replaceAll('/', '\\/')), true);
    assert.equal(containsMachinePath(path.replaceAll('/', '\\u002f')), true);
    assert.equal(containsMachinePath(path.replaceAll('/', '\\x2f')), true);
    assert.equal(containsMachinePath(encodeURIComponent(encodeURIComponent(path))), true);
  }
});

test('keeps portable instructions and synthetic coordinates valid', () => {
  for (const text of [
    '/path/to/ahe-query-launcher', '/synthetic/ahe-core/docs/STATUS.md',
    '/redacted/home/Library/Caches', '/redacted/runtime-temp/go-build',
    '/private/tmp/detective-desktop-trial.XXXXXX', '/private/tmp/fixture',
    'github.com/Yui-Qi-Tang/ahe-mcp', 'apps/detective/README.md',
  ]) assert.equal(containsMachinePath(text), false);
});

test('does not disclose a private path in a filename or inject log lines', () => {
  const privateName = ['backup', 'Users', 'fixture-account', 'report.txt'].join('/');
  assert.equal(publicFailureLabel(privateName), '[filename redacted]');
  assert.equal(publicFailureLabel('docs/report\n.txt'), '"docs/report\\n.txt"');
});
