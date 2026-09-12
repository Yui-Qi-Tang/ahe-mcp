// Checks publishable working-tree files, not ignored workspaces or Git history.
// This is a path regression check, not a credential or arbitrary archive scanner.
import { execFileSync } from 'node:child_process';
import { lstatSync, readFileSync, readlinkSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';

export function containsMachinePath(input) {
  let text = input;
  for (let pass = 0; pass < 3; pass++) {
    text = text.replace(/\\u([0-9a-f]{4})/gi, (_, hex) => String.fromCharCode(parseInt(hex, 16)))
      .replace(/\\x([0-9a-f]{2})/gi, (_, hex) => String.fromCharCode(parseInt(hex, 16)))
      .replace(/%([0-9a-f]{2})/gi, (_, hex) => String.fromCharCode(parseInt(hex, 16)))
      .replace(/\\\//g, '/');
  }
  text = text.replace(/\\+/g, '/');
  const temporaryPaths = [...text.matchAll(/[/](?:private[/])?tmp[/][^/\s"'<>\\()]+[.]([a-z0-9]{6})(?=[/\s"'<>\\()]|$)/gi)];
  return /[/]Users[/][^/\s]+/i.test(text)
    || /(?<![/]redacted)[/]home[/][^/\s]+/i.test(text)
    || /[/](?:private[/])?var[/]folders[/][^/\s]+[/][^/\s]+/i.test(text)
    || temporaryPaths.some(match => match[1] !== 'XXXXXX');
}

export function publicFailureLabel(name) {
  return containsMachinePath(name) ? '[filename redacted]' : JSON.stringify(name);
}

export function checkPublicPaths(root) {
  const names = execFileSync('git', ['ls-files', '--cached', '--others', '--exclude-standard', '-z'], { cwd: root })
    .toString().split('\0').filter(Boolean);
  const failures = [];
  for (const name of new Set(names)) {
    const path = resolve(root, name);
    let stat;
    try {
      stat = lstatSync(path);
    } catch (error) {
      if (error.code === 'ENOENT') continue; // Tracked deletion.
      throw error;
    }
    if (!stat.isFile() && !stat.isSymbolicLink()) continue;
    let content = stat.isSymbolicLink() ? readlinkSync(path) : readFileSync(path);
    if (!stat.isSymbolicLink() && name.endsWith('.gz')) content = gunzipSync(content);
    if (containsMachinePath(name) || containsMachinePath(content.toString())) failures.push(name);
  }
  return { checked: new Set(names).size, failures };
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const result = checkPublicPaths(fileURLToPath(new URL('../', import.meta.url)));
    if (result.failures.length) {
      // Never print the matched private path or source line into CI logs.
      for (const name of result.failures) console.error(`Machine path found: ${publicFailureLabel(name)}`);
      process.exitCode = 1;
    } else {
      console.log(`Public-path check passed (${result.checked} files; gzip contents included).`);
    }
  } catch {
    console.error('Public-path check could not read the publishable file set.');
    process.exitCode = 1;
  }
}
