-- Phase 8 Chunk 3: Rollback Migration
-- Migration: 000011_phase8_chunk3.down.sql

DELETE FROM role_permissions WHERE permission_code IN ('message.dlr_mapping.manage', 'message.queue.view', 'message.reconcile');
DELETE FROM permissions WHERE code IN ('message.dlr_mapping.manage', 'message.queue.view', 'message.reconcile');

DROP TABLE IF EXISTS provider_connection_sessions;
DROP TABLE IF EXISTS messaging_reconciliation_runs;
DROP TABLE IF EXISTS settlement_exceptions;
DROP TABLE IF EXISTS unmatched_dlrs;
DROP TABLE IF EXISTS provider_connection_dlr_mappings_history;
DROP TABLE IF EXISTS provider_connection_dlr_mappings;

