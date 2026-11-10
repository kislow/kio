#!/usr/bin/env node
// ki-diagram: builds tldraw drawings for the Obsidian vault from spec files,
// keeping hand edits, and renders them to PDF or SVG. Usually run as `ki diagram`.
import { requirements } from '../lib/requirements.mjs'

const USAGE = `usage:
  ki diagram build <spec.diagram.yaml> [--drawing file.md] [--dry-run] [--adopt | --base file.md] [--prefer drawing|spec] [--pdf]
  ki diagram check <spec.diagram.yaml>... [--drawing file.md]
  ki diagram render <drawing.md> [-o file.pdf]
  ki diagram render <drawing.md> --svg -o <dir> [--theme light|dark] [--json]
  ki diagram requirements [--json]`

class UsageError extends Error {}

function parse(args, flags, values) {
  const out = { positional: [] }
  for (let i = 0; i < args.length; i++) {
    const a = args[i]
    if (flags.includes(a)) out[a.replace(/^-+/, '')] = true
    else if (values.includes(a)) {
      if (i + 1 >= args.length) throw new UsageError(`${a} needs a value`)
      out[a.replace(/^-+/, '')] = args[++i]
    } else if (a.startsWith('-')) throw new UsageError(`unknown option ${a}`)
    else out.positional.push(a)
  }
  return out
}

async function main(argv) {
  const [command, ...args] = argv
  if (!command || command === 'help' || command === '-h' || command === '--help') {
    console.log(USAGE)
    return 0
  }
  if (command === 'requirements') {
    const checks = requirements()
    if (args.includes('--json')) console.log(JSON.stringify(checks))
    else for (const c of checks) console.log(`${c.ok ? 'ok ' : 'NO '} ${c.name}: ${c.detail}${c.ok ? '' : `\n    fix: ${c.fix}`}`)
    return checks.every((c) => c.ok) ? 0 : 1
  }
  // Building needs everything but the browser; rendering needs that too.
  const needed = requirements().filter((c) => !c.ok && (c.name !== 'browser' || command === 'render' || args.includes('--pdf')))
  if (needed.length) {
    for (const c of needed) console.error(`ki diagram: ${c.name}: ${c.detail}\n    fix: ${c.fix}`)
    return 1
  }
  const { apply, describePlan, failed, plan } = await import('../lib/plan.mjs')
  const pdfFor = (md) => md.replace(/\.md$/, '') + '.pdf'

  if (command === 'build') {
    const o = parse(args, ['--dry-run', '--adopt', '--pdf'], ['--drawing', '--base', '--prefer'])
    if (o.positional.length !== 1) throw new UsageError('build takes one spec')
    if (o.prefer && !['drawing', 'spec'].includes(o.prefer)) throw new UsageError('--prefer is drawing or spec')
    if (o.adopt && o.base) throw new UsageError('--adopt and --base are alternatives')
    const p = plan({ specPath: o.positional[0], drawingPath: o.drawing, basePath: o.base, adopt: o.adopt, prefer: o.prefer })
    if (!o['dry-run'] && !failed(p)) apply(p)
    for (const line of describePlan(p, { dryRun: o['dry-run'] })) console.log(line)
    if (failed(p)) return 1
    if (o.pdf && !o['dry-run']) await render(p.drawingPath, pdfFor(p.drawingPath))
    return 0
  }
  if (command === 'check') {
    const o = parse(args, [], ['--drawing'])
    if (!o.positional.length) throw new UsageError('check takes at least one spec')
    if (o.drawing && o.positional.length > 1) throw new UsageError('--drawing works with one spec only')
    let ok = true
    for (const specPath of o.positional) {
      try {
        const p = plan({ specPath, drawingPath: o.drawing })
        describePlan(p, { dryRun: true }).forEach((l) => console.log(l))
        ok &&= !failed(p)
      } catch (err) {
        console.log(err.message)
        ok = false
      }
    }
    return ok ? 0 : 1
  }
  if (command === 'render') {
    const o = parse(args, ['--svg', '--json'], ['-o', '--theme'])
    if (o.positional.length !== 1) throw new UsageError('render takes one drawing')
    if (!o.svg) {
      if (o.json || o.theme) throw new UsageError('--json and --theme need --svg')
      await render(o.positional[0], o.o ?? pdfFor(o.positional[0]))
      return 0
    }
    if (!o.o) throw new UsageError('--svg needs -o <dir>')
    if (o.theme && !['light', 'dark'].includes(o.theme)) throw new UsageError('--theme is light or dark')
    const { renderSvg } = await import('../lib/render.mjs')
    const pages = await renderSvg(o.positional[0], o.o, { theme: o.theme })
    if (o.json) console.log(JSON.stringify({ pages }))
    else for (const p of pages) console.log(p.file)
    return 0
  }
  throw new UsageError(`unknown command ${command}`)
}

async function render(drawingPath, pdfPath) {
  const { renderPdf } = await import('../lib/render.mjs')
  const pages = await renderPdf(drawingPath, pdfPath)
  console.log(`${pdfPath}: ${pages.length} page(s)`)
}

main(process.argv.slice(2)).then(
  (code) => { process.exitCode = code },
  (err) => {
    console.error(err instanceof UsageError ? `ki diagram: ${err.message}\n${USAGE}` : `ki diagram: ${err.message}`)
    process.exitCode = err instanceof UsageError ? 2 : 1
  },
)
