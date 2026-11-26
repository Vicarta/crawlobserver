# Quick 20: Depth writeback without mutation waits

Implementation committed as `1909550`; production rollout is blocked by a verified Retry Failed regression. The existing production app and ClickHouse were not changed.

- The foreground depth insert bypassed a genuine unrelated failed mutation in an isolated ClickHouse fixture, preserved PageRank revisions and page metadata, and produced a single current source row in the external-link report. The full Go race suite, targeted vet, build, and storage integration tests passed.
- A separate SQL reproduction confirmed that foreground replacement can copy a page still visible under `FINAL` while a same-session async retry DELETE is pending. The replacement part survives the earlier DELETE, unlike the previous depth mutation. `apply_mutations_on_fly` prevents that resurrection in isolation but rejects a pending legacy `joinGet` mutation, even when that mutation is for a different session.
- Keep the no-force app-only gates for the eventual rollout. Do not cancel old production mutations or launch test crawls. Resolving the blocker requires an explicit choice for legacy Retry Failed deletion behavior; ordinary depth finalization must remain independent of the mutation queue.
