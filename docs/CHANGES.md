# Cumulative Changes - Phase 8A Chunk 1

Generated: 2026-11-07 21:45:10 UTC

## Modified and Added Files
\\\
 M backend/cmd/seeddev/main.go
 M backend/internal/billing/service.go
 M backend/internal/customer/customer_test.go
 M backend/internal/ledger/ledger_test.go
 M backend/internal/ledger/service.go
 M backend/internal/masterdata/approval/approval_test.go
 M backend/internal/masterdata/approval/handler.go
 M backend/internal/masterdata/country/country_integration_test.go
 M backend/internal/org/org_test.go
 M backend/internal/rbac/platform_only.go
 M backend/internal/rbac/rbac_test.go
 M backend/internal/rbac/repository.go
 M backend/internal/routing/adapters.go
 M backend/internal/routing/engine.go
 M backend/internal/routing/handler.go
 M backend/internal/routing/models.go
 M backend/internal/routing/repository.go
 M backend/internal/routing/routing_test.go
 M backend/internal/routing/service.go
 M backend/internal/routing/snapshot.go
 M backend/internal/senderid/senderid_test.go
 M backend/internal/testkit/testkit.go
 M backend/migrations/000009_phase7_routing.down.sql
 M backend/migrations/000009_phase7_routing.up.sql
 M backend/pkg/httpx/app.go
 M backend/pkg/tenant/tenant_test.go
 M docs/ROUTING_DESIGN.md
 M frontend/e2e/phase3-masterdata.spec.ts
 M frontend/e2e/phase7-routing.spec.ts
?? backend/internal/messaging/
?? backend/migrations/000010_phase8_messaging.down.sql
?? backend/migrations/000010_phase8_messaging.up.sql
?? docs/CHANGES.md
?? docs/HANDOVER.md
?? docs/MESSAGING_DESIGN.md
?? docs/RBAC_MATRIX.md
?? scripts/make-handover-zip.ps1
?? scripts/test-e2e.ps1
\\\

## Diff Statistics
\\\
 backend/cmd/seeddev/main.go                        |   72 +-
 backend/internal/billing/service.go                |   27 +-
 backend/internal/customer/customer_test.go         |   51 +
 backend/internal/ledger/ledger_test.go             |  264 +++
 backend/internal/ledger/service.go                 |  155 +-
 .../internal/masterdata/approval/approval_test.go  |  149 +-
 backend/internal/masterdata/approval/handler.go    |   22 +-
 .../masterdata/country/country_integration_test.go |   70 +
 backend/internal/org/org_test.go                   |   37 +
 backend/internal/rbac/platform_only.go             |   42 +-
 backend/internal/rbac/rbac_test.go                 |   48 +
 backend/internal/rbac/repository.go                |    4 +-
 backend/internal/routing/adapters.go               |   17 +-
 backend/internal/routing/engine.go                 |  207 +-
 backend/internal/routing/handler.go                |  138 +-
 backend/internal/routing/models.go                 |   58 +-
 backend/internal/routing/repository.go             |   16 +-
 backend/internal/routing/routing_test.go           | 1974 +++++++++++++++++++-
 backend/internal/routing/service.go                |  157 +-
 backend/internal/routing/snapshot.go               |    2 +-
 backend/internal/senderid/senderid_test.go         |   39 +
 backend/internal/testkit/testkit.go                |    1 +
 backend/migrations/000009_phase7_routing.down.sql  |    3 +
 backend/migrations/000009_phase7_routing.up.sql    |   40 +-
 backend/pkg/httpx/app.go                           |   23 +
 backend/pkg/tenant/tenant_test.go                  |   30 +
 docs/ROUTING_DESIGN.md                             |   57 +
 frontend/e2e/phase3-masterdata.spec.ts             |  132 +-
 frontend/e2e/phase7-routing.spec.ts                |    4 +-
 29 files changed, 3576 insertions(+), 263 deletions(-)
\\\

## Major Additions in Phase 8A Chunk 1
1. **Migration 000010 (Phase 8 Messaging)**:
   - Partitioned tables: \messages\ and \message_parts\ partitioned by RANGE (\created_at\).
   - Pre-created monthly partitions and \DEFAULT\ catch-all partitions (\messages_default\, \message_parts_default\).
   - Unpartitioned lookup tables: \message_idempotency_lookups\ and \message_provider_lookups\ guaranteeing global uniqueness across month boundaries.
   - Transactional outbox table (\message_outbox\) and append-only audit tables (\message_attempts\, \message_events\, \message_dlrs\).
2. **Double-Entry Ledger Extensions**:
   - \PlaceHoldTx\, \CaptureTx\, \ReleaseTx\, \PostChargeTx\ supporting caller-provided transactions.
   - Idempotent capture replay verification with amount validation.
3. **Messaging Domain & Ingestion Pipeline (\ackend/internal/messaging\)**:
   - Dual-encoding engine (GSM-7 & UCS-2) with multi-part UDH concatenation.
   - Atomic submission: \PlaceHoldTx\ and \InsertMessageAtomic\ execute in the EXACT same database transaction.
   - In-memory rate limiting via token bucket.
   - Transactional outbox worker with \FOR UPDATE SKIP LOCKED\.
   - Dynamic \outing.Engine.Next()\ evaluation at dispatch time honoring live kill switches.
   - Strict failover policy: retryable rejects fail over to Next(); permanent fatal rejects release holds; ambiguous outcomes (\unknown\) retain holds and are NEVER auto-resent.
4. **Comprehensive Test Suite**:
   - 18 integration tests in \ackend/internal/messaging/messaging_test.go\ executed under \-race\ with zero skips and zero failures.
