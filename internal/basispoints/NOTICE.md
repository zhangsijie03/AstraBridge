# Source attribution

This package is adapted from hloolx/codex2api, whose README declares MIT License.
Original author of the Basispoints changes: hloolx. Source: https://github.com/hloolx/codex2api

Requested commits and required intervening fixes, in order:

- 9d02d3f5e5d69632ebb9590082a833c0a0916356 — initial Basispoints routing.
- c125e560eefb5fd15c995943eb1e111795b0635f — tool-loop identity and replay.
- 20ff3e860d9a149e2df731e37ba1d9b56ae053fc — envelope formatting and model access errors.
- d39f7e3697aab342e303bf4be0142e39b6a58515 — tool catalog and complete terminal items.
- 4dea83ec53b7668419edd2a9a9dd40fb55fdaacd — HTTPS image references.

Only the protocol package is imported; Sub2API supplies its own account setting,
OAuth credential lifecycle, proxy transport, usage recording and frontend.
The repositories have different layouts; this is a source port, not a Git merge
of the other application's deployment, database or account pool implementation.

Additional review reference: JaxsonWang/cpa-plugin-oai-basispoints at
05b2d97efa1bd117da6bd4d362d6e88f8e483680. Its tool/args envelope examples
were compared with this package. We retain scoped caches, incremental text
streaming and multiple-terminal-tool handling rather than its global call-ID
cache and single-transport extraction. No CPA plugin ABI is imported.
