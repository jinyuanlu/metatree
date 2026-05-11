# TODOs

Deferred work, captured during planning so context survives the conversation.

## v2: `worktree_post_create` shell hook

**What:** Add a `worktree_post_create` config field that runs a user-supplied shell command in the newly-created worktree, after `worktree_copy_files` has run but before the agent launches.

**Why:** The most-cited universal pain in the worktree workflow is "the worktree isn't runnable until I install dependencies" — Schumaker measured "10+ minutes, every single time" in his node_modules-heavy monorepo, and Anthropic's own Claude Code docs admit "depending on the project, developers have to copy over files that are not checked into version control or install dependencies, often making it not worth it for a change Claude finishes in 10 minutes." A shell hook lets users solve this themselves (`ln -s ../node_modules .`, `pnpm install --offline`, `cp -al ../node_modules .`, `uv sync`) without `mt` having to grow per-ecosystem logic.

**Why deferred from v1:**
- Security surface — a hook can do anything, including run arbitrary code from a hostile repo. The v1 file-copy step is bounded; a hook isn't.
- Per-repo `.mt.toml` override is the obvious sibling feature (otherwise everyone's global config has to know every project's hook), and that's a cross-cutting design decision better made separately.
- v1 is framed as the "quick win" — the hook is a week of design and security review, not a day of implementation.

**Design notes for whoever picks this up:**
- Hook executes with `MT_REPO` and `MT_WORKTREE` env vars pointing at parent and new-worktree paths.
- Stdout/stderr captured and reported as a `mt: post-create hook: ok (1.2s)` / `mt: post-create hook failed (exit 17)` line, mirroring the `worktree_copy_files` summary convention.
- Timeout (default 60s) to prevent hung hooks from blocking `mt new`.
- Whether to run hook from the worktree directory or repo root: worktree directory.
- Trust model: the hook lives in `~/.metatree/config.toml`, not in the repo, so it's user-trusted not repo-trusted. Good. But if we later add per-repo override, that crosses the trust line and needs explicit opt-in.

**Depends on / blocked by:** Nothing technically, but should land after v1 (`worktree_copy_files`) has telemetry showing whether users actually want more.

**Evidence anchor:**
- [Dave Schumaker: "Use git worktrees, they said. It'll be fun, they said."](https://daveschumaker.net/use-git-worktrees-they-said-itll-be-fun-they-said/) — "10+ minutes, every single time"
- [Claude Code Common Workflows docs](https://code.claude.com/docs/en/common-workflows) — official acknowledgment
