// Runs inside headless Chromium: mounts tldraw on the drawing and exports
// every page to SVG with tldraw's own exporter, so the PDF and SVGs show
// what Obsidian shows. Bundled by render.mjs; everything is served locally.
import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { Tldraw } from 'tldraw'
import { getAssetUrls } from '@tldraw/assets/selfHosted'

const assetUrls = getAssetUrls({ baseUrl: '/assets' })

async function exportPages(editor, darkMode) {
  const pages = []
  for (const page of editor.getPages()) {
    editor.setCurrentPage(page.id)
    await editor.fonts.loadRequiredFontsForCurrentPage(Infinity)
    await document.fonts.ready
    const ids = [...editor.getCurrentPageShapeIds()]
    if (!ids.length) continue
    const svg = await editor.getSvgString(ids, { background: true, padding: 48, darkMode })
    if (svg) pages.push({ name: page.name, svg: svg.svg, width: svg.width, height: svg.height })
  }
  return pages
}

window.kiDiagram = async ({ darkMode = false } = {}) => {
  const snapshot = await (await fetch('/snapshot.json')).json()
  return new Promise((resolve, reject) => {
    const onMount = (editor) => {
      editor.updateInstanceState({ isReadonly: true })
      exportPages(editor, darkMode).then(resolve, reject)
    }
    createRoot(document.getElementById('root')).render(createElement(Tldraw, { snapshot, assetUrls, onMount }))
  })
}
