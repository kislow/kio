## Working with the knowledge base

You have a knowledge base available through the `ki-kb` MCP server. It holds the conventions, past decisions and current state for the repo you are in, scoped automatically to what this session may see.

- Before planning any change, call `get_context` with a one-line description of the task. Treat what it returns as the team's established practice: follow its conventions and active decisions unless the user tells you otherwise.
- Before changing how a component works, call `decisions_for` with the component name, so you do not reverse a decision without saying so.
- If the knowledge base and the code disagree, trust the code, and point out the mismatch to the user.
- When a choice worth remembering is made in this session (a library, a pattern, a trade-off), say so explicitly at the end so it can be recorded with `/ki-decision`.

Notes saved with `vault_write` reach the knowledge base on the next sync. For meaning rather than names, search the knowledge base instead of `vault_search`.
