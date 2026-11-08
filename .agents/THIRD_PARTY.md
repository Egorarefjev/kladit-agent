# Third-party development skills

These are repository-local Codex skills, not runtime dependencies or global ChatGPT installations. Read only the skills relevant to the task.

| Skills                                                                                                     | Upstream                                                                                                                              | License                                         |
| ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| `code-review-and-quality`, `code-simplification`, `debugging-and-error-recovery`, `security-and-hardening` | [addyosmani/agent-skills](https://github.com/addyosmani/agent-skills/tree/301b02b12bff98e4688e6984af6556802322bbde)                   | MIT; a copy is included in each skill directory |
| `golang-pro`                                                                                               | [Jeffallan/claude-skills](https://github.com/Jeffallan/claude-skills/tree/078a25cc5bfd8011459be7b91d004e900c1d02d3/skills/golang-pro) | MIT; a copy is included in the skill directory  |

All upstream content is copied without modification. Go references and the security skill's local hardening reference are included. The shared security/performance checklists live in `.agents/references` so upstream `../../references` paths resolve.

[skill-sources.json](skill-sources.json) records each copied file's source repository, exact commit, upstream path and SHA-256 of its bytes. It is a provenance manifest, not a skills CLI lockfile. The project's runtime remains licensed under the root LICENSE; upstream notices apply to the vendored development material.

For an intentional skill update, review the upstream diff and referenced files, copy from an explicit commit, preserve licenses, refresh manifest hashes, and recheck compatibility with [AGENTS.md](../AGENTS.md). Other skills mentioned by upstream are not installed automatically.
