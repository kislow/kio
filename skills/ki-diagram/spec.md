# Drawing spec format

A spec is YAML (JSON also works). `ki diagram build` checks all of it and lists every problem before writing anything.

```yaml
title: ki CLI                      # the drawing's name inside tldraw
tags: [diagram, kio, cli]   # frontmatter tags on a new drawing
pages:
  - name: Starting a session       # page tab; also its id, so keep it once published
    intro:                         # title and one or two lines of context, placed above the content
      title: "ki: one command for a scoped agent session"
      about: ki reads which repo you are in, looks it up in projects.yaml, and starts your agent.
    outro:                         # optional: notes for presenting the page, placed under it
      title: Explaining it in one minute
      body: ...
    backdrop: true                 # soft grey panel behind the page (default true)
    shapes:                        # drawn in this order, later on top
      - {group: inputs, x: 0, y: 0, w: 320, h: 820, label: What ki reads}
      - {box: repo, x: 45, y: 50, label: "this repo\ngit remote"}
      - {stack: brains, x: 840, y: 640, label: FalkorDB brains}
      - {box: ki, x: 480, y: 330, w: 240, h: 130, label: ki, color: path}
      - {box: refuse, x: 380, y: 780, label: refuses, dash: dashed}
      - {box: mapped, x: 380, y: 140, w: 260, h: 170, label: "repo in\nprojects.yaml?", geo: diamond}
      - {caption: c1, x: 400, y: 800, w: 460, text: "Claude gets --mcp-config", align: start}
      - {line: r-k, from: repo, to: ki, label: which project}
      - {line: k-b, from: ki, to: brains, label: asks, color: path, head: true, bend: 40, label_position: 0.35}
```

## Shapes

Each shape is one mapping whose kind key holds its id. Ids are letters, digits and dashes, unique on the page; the shape's tldraw id is `shape:<page>-<id>`, so an id must not change once the drawing exists.

| Kind | Required | Optional | Notes |
| --- | --- | --- | --- |
| `box` | `x`, `y` | `w` (230), `h` (110), `label`, `color`, `geo`, `dash`, `fill`, `size`, `label_color` | A labelled shape; `geo: diamond` for a decision |
| `stack` | as `box` | as `box` | A box with two outlines behind it: several of the same thing |
| `group` | `x`, `y`, `w`, `h`, `label` | | Dashed light-red frame, its name underneath |
| `caption` | `x`, `y`, `w`, `text` | `align` (`start`, `middle`, `end`) | Small grey text: the one thing a page must say in words |
| `line` | `from`, `to` | `label`, `color`, `dash`, `head`, `bend`, `label_position` | Joins the centres of two boxes, stacks or groups, bound at both ends |

- `color`: `core` (black, the default), `path` (blue), `external` (green), or any tldraw colour.
- `dash`: `draw` (default), `solid`, `dashed`, `dotted`, `none`. `fill`: `semi` (default), `none`, `solid`, `pattern`. `size`: `s`, `m` (default), `l`, `xl`.
- `head: true` puts an arrowhead at `to`; `bend` curves the line (pixels); `label_position` is 0 to 1 along it (default 0.5).
- `\n` in a quoted label starts a new line.

## Conventions

- **Intro title page first.** Page one says what the drawing explains and why it matters (for example "Why kio" or "kio at a glance"). Every page has an `intro`; the build warns when one is missing.
- **One concept per page.** If a page needs two intros, it is two pages. Six to twelve boxes is plenty.
- **Sentence case** for every title, label and caption; proper names keep their own case (Obsidian Sync, FalkorDB, ki).
- **Two or three words per box.** Put detail in a caption, not in the box.
- **Colour means the same on every page:** `core` for the system's own parts, `path` for the path a request takes, `external` for services it runs on. Say so on a glossary page if the drawing has one.
- **Lines say what flows** ("push", "context pack", "every 5 min"); add `head: true` when direction matters.
- **Layout:** columns about 400 apart and rows about 250 apart for default boxes; groups 40 to 50 wider than what they hold on each side; leave space for line labels; keep x and y on a 5 or 10 grid.

## How hand edits are kept

`.ki-diagram/<Title>.base.json` holds what the last build wrote. Each build compares, record by record and field by field, that snapshot (base), the drawing as it is now (the user's edits), and what the spec builds today:

| Drawing vs base | Spec vs base | Result |
| --- | --- | --- |
| same | changed | spec change applied |
| changed | same | hand edit kept (including shapes added or deleted by hand) |
| changed | changed the same way | nothing to do |
| changed | changed differently | conflict: nothing written until `--prefer drawing` or `--prefer spec` |

A box moved by hand still takes a new label from the spec: only the same field changed on both sides is a conflict. Z-order (`index`) is never compared or overwritten for a shape the drawing already has, because tldraw rewrites those keys on its own (it restacks arrows when it opens a file). tldraw's text-direction marker on an edited label is not an edit either. Session records the plugin keeps in the file (camera, pointer, user) are left alone, as are the drawing's frontmatter and its plugin uuid.
