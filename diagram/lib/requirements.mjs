// What ki-diagram needs on this machine, checked with node built-ins only so
// it can run (and explain the fix) before any dependency is loaded.
import { existsSync, readFileSync } from 'node:fs'
import { homedir, platform } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

export const ROOT = dirname(dirname(fileURLToPath(import.meta.url)))
export const MIN_NODE = 22
const INSTALL = `make -C ${dirname(ROOT)} diagram`

export function requirements(env = process.env) {
  const node = Number(process.versions.node.split('.')[0])
  const checks = [{ name: 'node', ok: node >= MIN_NODE, detail: `node ${process.versions.node}`, fix: `install node ${MIN_NODE} or newer` }]
  const missing = dependencyProblems()
  checks.push({ name: 'packages', ok: missing.length === 0, detail: missing.length ? missing.join(', ') : 'tldraw, playwright and the rest at their pinned versions', fix: INSTALL })
  checks.push(browserCheck(env))
  return checks
}

// Every dependency installed at exactly the version package.json pins.
function dependencyProblems() {
  const pinned = JSON.parse(readFileSync(join(ROOT, 'package.json'), 'utf8')).dependencies
  const problems = []
  for (const [name, version] of Object.entries(pinned)) {
    const manifest = join(ROOT, 'node_modules', name, 'package.json')
    if (!existsSync(manifest)) { problems.push(`${name} missing`); continue }
    const installed = JSON.parse(readFileSync(manifest, 'utf8')).version
    if (installed !== version) problems.push(`${name} ${installed}, want ${version}`)
  }
  return problems
}

// The headless Chromium the pinned Playwright drives, or KI_DIAGRAM_CHROME.
export function browserCheck(env = process.env) {
  if (env.KI_DIAGRAM_CHROME) {
    const ok = existsSync(env.KI_DIAGRAM_CHROME)
    return { name: 'browser', ok, detail: `KI_DIAGRAM_CHROME=${env.KI_DIAGRAM_CHROME}${ok ? '' : ' does not exist'}`, fix: 'point KI_DIAGRAM_CHROME at a Chrome or Chromium binary, or unset it', path: env.KI_DIAGRAM_CHROME }
  }
  const dir = headlessShellDir(env)
  const ok = dir !== null && existsSync(join(dir, 'INSTALLATION_COMPLETE'))
  return { name: 'browser', ok, detail: ok ? `headless Chromium in ${dir}` : 'the pinned headless Chromium is not installed (needed for PDFs only)', fix: INSTALL }
}

function headlessShellDir(env) {
  const browsersFile = join(ROOT, 'node_modules', 'playwright-core', 'browsers.json')
  if (!existsSync(browsersFile)) return null
  const shell = JSON.parse(readFileSync(browsersFile, 'utf8')).browsers.find((b) => b.name === 'chromium-headless-shell')
  if (!shell) return null
  return join(browsersRoot(env), `chromium_headless_shell-${shell.revision}`)
}

// Where Playwright keeps its browsers, by its own rules.
function browsersRoot(env) {
  if (env.PLAYWRIGHT_BROWSERS_PATH && env.PLAYWRIGHT_BROWSERS_PATH !== '0') return env.PLAYWRIGHT_BROWSERS_PATH
  if (env.PLAYWRIGHT_BROWSERS_PATH === '0') return join(ROOT, 'node_modules', 'playwright-core', '.local-browsers')
  if (platform() === 'darwin') return join(homedir(), 'Library', 'Caches', 'ms-playwright')
  if (platform() === 'win32') return join(env.LOCALAPPDATA ?? homedir(), 'ms-playwright')
  return join(env.XDG_CACHE_HOME ?? join(homedir(), '.cache'), 'ms-playwright')
}
