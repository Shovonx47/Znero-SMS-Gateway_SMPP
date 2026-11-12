# Znero International — Open Issues & Operational Observations (`docs/OPEN_ISSUES.md`)

## OI-001: External IDE / File Watcher Buffer Reversion Observed During Unattended Run
- **Category**: Host Environment / Tooling Observation
- **Severity**: Informational (no product code defect)
- **Observed**: At several timestamps between `07:20` and `08:23 +06:00` (notably around `07:22`, `07:29`, `07:39`, `07:44`, `07:52`, and `08:00:01 +06:00`), a small set of recently opened files (`cmd/api/main.go`, `cmd/e2eblackbox/main.go`, `cmd/e2eblackbox/smpp_proto.go`, `internal/ledger/{service,handler}.go`, `internal/messaging/{repository,dlr}.go`, `internal/smpp/client.go`, `scripts/dev-up.ps1`, `docs/BUGS_FOUND.md`) were overwritten back to an earlier snapshot state on the Windows host by an external IDE/editor buffer or workspace watcher outside the agent shell.
- **Mitigation**: All reverted fixes were restored cleanly, verified via `Select-String` checksums immediately before building binaries and running tests, and confirmed intact in both compiled `bin\` binaries and source files on disk prior to final verification. Operators opening this repository in an editor while running automated CLI scripts should disable auto-save of stale unsaved tabs or close dirty editor buffers before running automation.

