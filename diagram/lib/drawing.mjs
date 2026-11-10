// Reads and writes the Obsidian tldraw plugin's file format: Markdown
// frontmatter, then the tldraw document as JSON between two marker lines.
import { createHash, randomUUID } from 'node:crypto'
import { mkdirSync, readFileSync, renameSync, writeFileSync } from 'node:fs'
import { basename, dirname, join } from 'node:path'
import { PLUGIN_VERSION, TLDRAW_VERSION } from './tldraw.mjs'

const START = '```json !!!_START_OF_TLDRAW_DATA__DO_NOT_CHANGE_THIS_PHRASE_!!!\n'
const END = '\n!!!_END_OF_TLDRAW_DATA__DO_NOT_CHANGE_THIS_PHRASE_!!!\n```'

// Parses a drawing's text. `before` and `after` are everything around the
// data block, kept as they are so hand-added frontmatter survives a rebuild.
export function parseDrawing(text, name = 'drawing') {
  const start = text.indexOf(START)
  const end = text.indexOf(END, start)
  if (start < 0 || end < 0) throw new Error(`${name} is not an Obsidian tldraw drawing (no tldraw data block)`)
  let data
  try {
    data = JSON.parse(text.slice(start + START.length, end))
  } catch (err) {
    throw new Error(`${name}: the tldraw data block is not valid JSON: ${err.message}`)
  }
  if (!data?.raw?.schema || !Array.isArray(data.raw.records)) throw new Error(`${name}: the tldraw data has no schema or records`)
  return { before: text.slice(0, start), after: text.slice(end + END.length), meta: data.meta ?? {}, raw: data.raw }
}

export function readDrawing(path) {
  const text = readFileSync(path, 'utf8')
  return { ...parseDrawing(text, basename(path)), hash: hashText(text) }
}

export const hashText = (text) => createHash('sha256').update(text).digest('hex')

export function frontmatter(tags) {
  return '---\ntldraw-file: true\ntags:\n' + tags.map((t) => `  - ${t}\n`).join('') + '---\n\n'
}

// Formats a drawing the way the plugin does: tab-indented JSON, plugin meta first.
export function formatDrawing({ before, after = '', meta, schema, records }) {
  const data = {
    meta: { uuid: meta?.uuid ?? randomUUID(), 'plugin-version': meta?.['plugin-version'] ?? PLUGIN_VERSION, 'tldraw-version': meta?.['tldraw-version'] ?? TLDRAW_VERSION },
    raw: { tldrawFileFormatVersion: 1, schema, records },
  }
  return before + START + JSON.stringify(data, null, '\t') + END + after
}

// Writes through a temporary file and a rename, so Obsidian never sees half a drawing.
export function writeAtomic(path, text) {
  mkdirSync(dirname(path), { recursive: true })
  const tmp = join(dirname(path), `.${basename(path)}.${process.pid}.tmp`)
  writeFileSync(tmp, text)
  renameSync(tmp, path)
}
