---
name: ki-diagram
description: Draw or update a tldraw drawing in the Obsidian vault from a spec file, keeping hand edits, and render it to PDF. Use for architecture, flow and explainer drawings the user opens and edits in Obsidian.
argument-hint: <what to draw, or the drawing to update>
disable-model-invocation: true
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__search mcp__ki-kb__decisions_for Bash(ki status *) Bash(ki diagram *) Bash(ki vault push *)
---

Draw: $ARGUMENTS

A drawing is declared as data: a spec file lists pages, boxes, groups, captions and lines, and `ki diagram build` turns it into a tldraw drawing the Obsidian tldraw plugin opens. The user edits drawings by hand in Obsidian; the build keeps those edits. You change the spec, never the drawing file.

## 1. Know the subject

When the `ki-kb` tools are offered, call `get_context` for the project the drawing is about, and `decisions_for` on each component you will draw; skip silently when they are not. Draw what is true now: names, flows and numbers from the code, docs and decisions, not from memory. If a fact you need is missing, ask one question rather than inventing it.

## 2. Find the files

Run `ki status --json`. If the identity has `vault: false`, nothing goes in the vault: ask the user for a folder outside it. Otherwise the drawing lives in `$KI_VAULT/diagrams/projects/<project>/` (if `vault_writable` is false, `KI_VAULT` is unset or not a git clone: ask where to save it). For a drawing called `<Title>` (the user's own name for it, such as `ki CLI` or `Vault Pusher`):

```text
<Title>.md                          the drawing; Obsidian opens it, the user edits it
<Title>.pdf                         rendered copy, one page per drawing page
.ki-diagram/<Title>.diagram.yaml    the spec: the only file you edit
.ki-diagram/<Title>.base.json       what the last build wrote; never edit or delete it
```

Run `ki diagram check ".ki-diagram/<Title>.diagram.yaml"` first when the drawing exists. It reports hand edits it will keep, and refuses on a conflict or when there is no snapshot.

## 3. Plan the pages, then confirm

Show the user the page list (name, the one concept it explains, roughly what is on it) and wait for approval before writing a large new drawing. Follow the conventions in [spec.md](spec.md): an intro title page first, one concept per page, sentence-case labels, two or three words per box, colour by role.

## 4. Write the spec and build

Write or edit the spec using the format in [spec.md](spec.md), then:

```bash
ki diagram build ".ki-diagram/<Title>.diagram.yaml" --dry-run   # what would change
ki diagram build ".ki-diagram/<Title>.diagram.yaml" --pdf       # write the drawing and the PDF
```

Fix every problem the build lists; it reports them all at once. When a shape exists in the drawing, keep its id: renaming an id removes the shape and adds a new one, and the hand edits on it are lost.

## 5. Look at it

Read the PDF and check each page: nothing overlaps, labels fit their boxes, lines do not cross through boxes, groups enclose what they name, and the page reads in the order the intro promises. Adjust coordinates in the spec and build again until it is clean.

## 6. When the build refuses

- **Conflict:** a shape was edited by hand and the spec changes the same field. Show the user both values. Pass `--prefer drawing` or `--prefer spec` only with the user's choice; never resolve it yourself.
- **No snapshot:** the drawing was not written by `ki diagram` here, or the snapshot was lost. `--adopt` keeps the drawing exactly as it is and treats every difference from the spec as a hand edit; `--base <file.md>` names the file ki-diagram last wrote. Ask before using either.
- **Changed while merging:** Obsidian saved the drawing during the build. Run it again.

Ask the user to close the drawing in Obsidian before a build that changes it; an open drawing saved afterwards would put the old shapes back.

## 7. Save

Show the user what changed (the build's summary and the PDF path). After approval, run `ki vault push -m "diagram: <Title>"`; if it stops on a conflict, show its message and stop.

## Rules

- Never edit a drawing's `.md` file or `.ki-diagram/*.base.json` directly, and never delete a snapshot to get past a refusal.
- Never mention the harness, ki, or this skill in code, commits, PRs, issues or review comments of project repos. Drawings live in the vault, not in project repos.
