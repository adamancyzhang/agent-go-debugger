#!/usr/bin/env node
// npm launcher for agent-go-debugger.
//
// The Go CLI is compiled ahead of time per platform/arch; each build is
// shipped as its own optional dependency package
// (@adamancyzhang/agent-go-debugger-<platform>-<arch>) so npm installs only
// the binary that matches the current machine. Nothing else is required:
// no Go toolchain, no Python, no runtime dependencies.
//
// AGD_PLATFORM / AGD_ARCH can override the platform/arch selection (used by
// the release tests to exercise the mapping for every target).
import { createRequire } from 'module';
import { spawnSync } from 'child_process';
import { fileURLToPath } from 'url';
import path from 'path';

const require = createRequire(import.meta.url);

const platform = process.env.AGD_PLATFORM || process.platform;
const arch = process.env.AGD_ARCH || process.arch;

const TARGETS = {
  'darwin-arm64': 'darwin-arm64',
  'darwin-x64': 'darwin-x64',
  'linux-arm64': 'linux-arm64',
  'linux-x64': 'linux-x64',
  // Delve's native backend has no windows/arm64 build; Windows-on-ARM
  // machines run the x64 binary through emulation.
  'win32-arm64': 'win32-x64',
  'win32-x64': 'win32-x64',
};
const key = `${platform}-${arch}`;
const target = TARGETS[key];
if (!target) {
  console.error(
    `agent-go-debugger: unsupported platform "${key}". ` +
    'Supported: darwin (arm64/x64), linux (arm64/x64), win32 (arm64/x64).'
  );
  process.exit(1);
}

const pkgName = `@adamancyzhang/agent-go-debugger-${target}`;
const exe = platform === 'win32' ? 'agent-go-debugger.exe' : 'agent-go-debugger';

let binPath;
try {
  binPath = require.resolve(`${pkgName}/bin/${exe}`);
} catch {
  // Development fallback: a locally built binary (go build -o dist/...).
  const local = path.join(
    path.dirname(fileURLToPath(import.meta.url)),
    '..',
    'dist',
    exe
  );
  if (require('fs').existsSync(local)) {
    binPath = local;
  } else {
    console.error(
      `agent-go-debugger: platform binary package ${pkgName} is not installed.\n` +
      'Reinstall with --force so npm fetches the matching optional dependency: ' +
      'npm i -g @adamancyzhang/agent-go-debugger --force'
    );
    process.exit(1);
  }
}

// path may point into node_modules/<pkg>/bin/<exe>
const result = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`agent-go-debugger: failed to run ${binPath}: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
