#!/usr/bin/env node
// Release build for the npm distribution.
//
// Compiles the Go CLI for every supported platform/arch into per-platform
// npm packages (npm/<name>/), which the main package consumes as optional
// dependencies (esbuild-style). Also lints SKILL.md frontmatter and smoke
// tests the current platform's binary through the npm launcher.
//
//   node scripts/build.js          build all targets
//   node scripts/build.js --smoke  build only the current target + smoke test
//
// Publishing order (scripts/publish.sh): publish every platform package
// first, then the main package.
import { execFileSync, execSync } from 'child_process';
import fs from 'fs';
import os from 'os';
import path from 'path';
import { fileURLToPath } from 'url';

const ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), '..');
const NPM_DIR = path.join(ROOT, 'npm');
const PKG = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
const VERSION = PKG.version;

// goos/goarch -> npm platform/arch tuple used in the package name.
const TARGETS = [
  { goos: 'darwin', goarch: 'arm64' },
  { goos: 'darwin', goarch: 'amd64' },
  { goos: 'linux', goarch: 'arm64' },
  { goos: 'linux', goarch: 'amd64' },
  { goos: 'windows', goarch: 'amd64' },
];
const npmPlatform = { darwin: 'darwin', linux: 'linux', windows: 'win32' };
const npmArch = { arm64: 'arm64', amd64: 'x64' };

function tagOf(t) {
  return `${t.goos}-${t.goarch}`;
}
function findTarget(tag) {
  return TARGETS.find((t) => tagOf(t) === tag);
}

function pkgName(t) {
  return `@adamancyzhang/agent-go-debugger-${npmPlatform[t.goos]}-${npmArch[t.goarch]}`;
}
function exeName(t) {
  return t.goos === 'windows' ? 'agent-go-debugger.exe' : 'agent-go-debugger';
}

function goBuild(t) {
  const destDir = path.join(NPM_DIR, pkgName(t).replace(/^@[^/]+\//, ''), 'bin');
  fs.mkdirSync(destDir, { recursive: true });
  const out = path.join(destDir, exeName(t));
  console.log(`build ${t.goos}/${t.goarch} -> ${path.relative(ROOT, out)}`);
  execFileSync('go', ['build', '-trimpath', '-o', out, '.'], {
    cwd: ROOT,
    env: {
      ...process.env,
      GOOS: t.goos,
      GOARCH: t.goarch,
      CGO_ENABLED: '0',
    },
    stdio: 'inherit',
  });
  fs.chmodSync(out, 0o755);
  return out;
}

function writePlatformPackage(t) {
  const dir = path.join(NPM_DIR, pkgName(t).replace(/^@[^/]+\//, ''));
  const manifest = {
    name: pkgName(t),
    version: VERSION,
    description: `${t.goos}/${t.goarch} binary for @adamancyzhang/agent-go-debugger (Go debugging CLI via Delve headless)`,
    os: [npmPlatform[t.goos]],
    cpu: [npmArch[t.goarch]],
    bin: { 'agent-go-debugger': `bin/${exeName(t)}` },
    files: ['bin'],
    license: 'MIT',
    scripts: {
      // npm publish triggers this: build only THIS platform's binary, so a
      // publish never ships stale output and never rebuilds other platforms.
      prepublishOnly: `node ../../scripts/build.js --one ${tagOf(t)}`,
    },
    publishConfig: {
      access: 'public',
      registry: 'https://registry.npmjs.org/',
    },
  };
  fs.writeFileSync(
    path.join(dir, 'package.json'),
    JSON.stringify(manifest, null, 2) + '\n'
  );
  fs.writeFileSync(path.join(dir, 'README.md'), `# ${pkgName(t)}\n\nPlatform binary for @adamancyzhang/agent-go-debugger. Install the main package instead.\n`);
}

function checkSkillFrontmatter() {
  execSync('bash scripts/check-skill.sh', { cwd: ROOT, stdio: 'inherit' });
}

function currentTarget() {
  const m = os.platform() === 'win32' ? 'windows' : os.platform();
  const a = os.arch() === 'x64' ? 'amd64' : os.arch() === 'arm64' ? 'arm64' : os.arch();
  return { goos: m, goarch: a };
}

function smokeTest(t) {
  const binPath = path.join(
    NPM_DIR,
    pkgName(t).replace(/^@[^/]+\//, ''),
    'bin',
    exeName(t)
  );
  console.log(`smoke ${t.goos}/${t.goarch}`);
  const out = execFileSync(binPath, ['version'], { encoding: 'utf8' });
  if (!/^agent-go-debugger /.test(out)) {
    console.error(`smoke failed for ${t.goos}/${t.goarch}: ${out}`);
    process.exit(1);
  }
}

const onlySmoke = process.argv.includes('--smoke');
const oneIdx = process.argv.indexOf('--one');
const oneTag = oneIdx >= 0 ? process.argv[oneIdx + 1] : null;
checkSkillFrontmatter();

if (oneTag) {
  const t = findTarget(oneTag);
  if (!t) {
    console.error(`unknown target "${oneTag}" — expected one of: ${TARGETS.map(tagOf).join(', ')}`);
    process.exit(1);
  }
  console.log(`build single target ${oneTag}`);
  goBuild(t);
  writePlatformPackage(t);
  console.log('done');
} else if (onlySmoke) {
  const t = currentTarget();
  if (!TARGETS.some((x) => x.goos === t.goos && x.goarch === t.goarch)) {
    console.error(`current platform ${t.goos}/${t.goarch} is not a release target`);
    process.exit(1);
  }
  const out = goBuild(t);
  writePlatformPackage(t);
  smokeTest(t);
  console.log('smoke OK');
} else {
  for (const t of TARGETS) {
    goBuild(t);
    writePlatformPackage(t);
  }
  const cur = currentTarget();
  if (TARGETS.some((x) => x.goos === cur.goos && x.goarch === cur.goarch)) {
    smokeTest(cur);
  }
  console.log('build complete — platform packages under npm/');
  console.log('publish with: bash scripts/publish.sh  (platform packages first, main package last)');
}
