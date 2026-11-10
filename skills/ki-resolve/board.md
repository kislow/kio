# Moving a card on the GitHub Project board

Optional: skip this file when `board` from `ki status --json` is null or empty. The issue and PR work the same without a board.

`board` from `ki status --json` is the project's title. Owner is the repo owner.

1. Find the project number: `gh project list --owner <owner> --format json` and match the title.
2. Find the issue's item: `gh project item-list <number> --owner <owner> --format json --limit 500`, match `content.number` to the issue number. Note the item `id`.
3. Find the Status field and its options: `gh project field-list <number> --owner <owner> --format json`. Note the field `id`, and the option `id`s for the target column (match "In progress", "In review" or "Done" case-insensitively; use the closest existing name).
4. Get the project's node id: `gh project view <number> --owner <owner> --format json`, field `id`.
5. Move it: `gh project item-edit --id <item-id> --project-id <project-node-id> --field-id <status-field-id> --single-select-option-id <option-id>`.

If any call fails with a scope error, tell the user to run `gh auth refresh -s project` once and continue without moving the card. Never fail the issue because the board could not be updated.
