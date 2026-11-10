// The tldraw schema every drawing is checked against. Records only become a
// drawing after passing through tldraw's own store, so an invalid shape fails
// here rather than in Obsidian.
import * as tl from '@tldraw/tlschema'
import { Store } from '@tldraw/store'

export { tl }

export const schema = tl.createTLSchema({ shapes: tl.defaultShapeSchemas, bindings: tl.defaultBindingSchemas })

// Records that make up the picture. The rest (camera, pointer, instance, user)
// is per-session state the Obsidian plugin writes back; it is never compared.
export const CONTENT_TYPES = new Set(['document', 'page', 'shape', 'binding', 'asset'])

export const isContent = (record) => CONTENT_TYPES.has(record.typeName)

export function newStore() {
  return new Store({
    schema,
    props: { defaultName: '', assets: { upload: async () => '', resolve: async () => '' }, onMount: () => {} },
  })
}

// Puts records through a fresh store: validates every one, and lets tldraw's
// integrity checker add the session records a usable document needs.
export function validated(records) {
  const store = newStore()
  store.mergeRemoteChanges(() => store.put(records))
  return JSON.parse(JSON.stringify(store.allRecords()))
}

export const serializedSchema = () => JSON.parse(JSON.stringify(schema.serialize()))

// Brings records saved under another schema version to this one. A drawing
// saved by a newer tldraw cannot be migrated down, and is refused.
export function migrate(rawSchema, records) {
  const result = schema.migrateStoreSnapshot({ schema: rawSchema, store: Object.fromEntries(records.map((r) => [r.id, r])) })
  if (result.type !== 'success') {
    throw new Error(`cannot read this drawing with tldraw ${TLDRAW_VERSION} (${result.reason}); it was probably saved by a newer tldraw`)
  }
  return Object.values(result.value)
}

// The tldraw version the Obsidian plugin bundles; package.json pins the same.
export const TLDRAW_VERSION = '5.4.0'
export const PLUGIN_VERSION = '1.32.0'
