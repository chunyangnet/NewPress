package database

import (
	"context"
	"encoding/binary"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"newpress.local/newpress/internal/plugindispatch"
)

// This exact trigger body is installed by append-only migration 0064. Checking
// it and the always-enabled, unconditional BEFORE INSERT/UPDATE row trigger in
// the SAME statement snapshot as the rows binds reuse to the current contract.
// Invalid contracts fail closed; changed catalogue versions invalidate hints.
// Row xmin/ctid also catch transient triggers that restore OLD's token before
// disappearing. These hints rely on PostgreSQL tuple integrity, not checksums
// of underlying storage pages; a cold read always decodes the actual bytes.
const webhookTokenFunction = `
BEGIN
    NEW.validation_token := pg_catalog.gen_random_uuid();
    RETURN NEW;
END;
`

const currentWebhookRows = `
WITH known AS (
    SELECT * FROM unnest($2::text[],$3::uuid[],$6::text[]) AS k(id,token,tuple)
), integrity AS MATERIALIZED (
    SELECT coalesce((
        SELECT convert_to(concat_ws(':',t.tgrelid,t.oid,t.xmin,t.ctid,p.oid,p.xmin,p.ctid,
            pg_catalog.pg_relation_filenode(t.tgrelid),pg_catalog.pg_postmaster_start_time(),
            floor(pg_catalog.pg_snapshot_xmax(pg_catalog.pg_current_snapshot())::text::numeric/4294967296)), 'UTF8')
        FROM pg_catalog.pg_trigger t
        JOIN pg_catalog.pg_proc p ON p.oid=t.tgfoid
        JOIN pg_catalog.pg_language l ON l.oid=p.prolang
        WHERE t.tgrelid='np_plugin_webhook_intents'::regclass
          AND t.tgname='np_plugin_webhook_validation_token'
          AND t.tgfoid='np_plugin_webhook_validation_token()'::regprocedure
          AND t.tgenabled='A' AND NOT t.tgisinternal AND t.tgtype=23
          AND t.tgnargs=0 AND t.tgqual IS NULL AND t.tgattr=''::int2vector
          AND p.prosrc=$4 AND l.lanname='plpgsql' AND NOT p.prosecdef
          AND p.prorettype='trigger'::regtype AND p.provolatile='v'
          AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_trigger other
              WHERE other.tgrelid=t.tgrelid AND NOT other.tgisinternal AND other.oid<>t.oid)
    ),''::bytea) AS stamp
), current_rows AS (
    SELECT true AS present,id,job_id,slot,version,state,validation_token,data,
           concat_ws(':',tableoid,xmin,ctid) AS tuple
    FROM np_plugin_webhook_intents ORDER BY id LIMIT $1
)
SELECT integrity.stamp,coalesce(i.present,false),coalesce(i.id,''),coalesce(i.job_id,''),coalesce(i.slot,''),
       coalesce(i.version,0),coalesce(i.state,''),i.validation_token,coalesce(i.tuple,''),coalesce(octet_length(i.data),0),
       CASE WHEN integrity.stamp=$5 AND i.validation_token=k.token AND i.tuple=k.tuple THEN NULL ELSE i.data END
FROM integrity LEFT JOIN current_rows i ON integrity.stamp<>''::bytea
LEFT JOIN known k ON k.id=i.id`

const webhookTokenTransferThreshold = 8 << 20

func scanCurrentWebhookIntents(ctx context.Context, tx pgx.Tx, memo *webhookValidationMemo, visit func(plugindispatch.Intent)) (int, int, error) {
	previous, previousStamp, hints := memo.snapshotWithHints()
	var ids, tokens, tuples []byte
	bytes := 0
	for _, entry := range previous {
		bytes += len(entry.raw)
	}
	// Small collections cost less to compare directly than to send an ID/token
	// join input. Both paths check the same trigger contract and current rows.
	if bytes > webhookTokenTransferThreshold {
		ids, tokens, tuples = hints.parameters()
	}
	rows, err := queryCurrentWebhookRows(ctx, tx, ids, tokens, tuples, previousStamp)
	if err != nil {
		return 0, 0, plugindispatch.ErrUnavailable
	}
	defer rows.Close()
	next := make(map[string]*validatedWebhook, len(previous))
	slots := map[string]int{}
	total, used, size, retained := 0, 0, 0, 0
	stamp := ""
	// Scan targets are reused only while a row is borrowed. Retained maps use
	// the validated intent's owned strings; no driver buffer escapes Next.
	var id, job, slot, state, tuple pgtype.DriverBytes
	var present bool
	var version int64
	var token pgtype.UUID
	var length int
	var contract, raw pgtype.DriverBytes
	targets := []any{&contract, &present, &id, &job, &slot, &version, &state, &token, &tuple, &length, &raw}
	for rows.Next() {
		if ctx.Err() != nil {
			return 0, 0, plugindispatch.ErrUnavailable
		}
		if err := rows.Scan(targets...); err != nil {
			return 0, 0, plugindispatch.ErrUnavailable
		}
		if len(contract) == 0 {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		if stamp == "" {
			stamp = string(contract)
		} else if string(contract) != stamp {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		if !present {
			continue // Empty table still returned and checked its trigger contract.
		}
		if !token.Valid || length <= 0 || length > plugindispatch.MaxRecordBytes || total >= plugindispatch.MaxRecords {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		cached := previous[string(id)]
		var checked *validatedWebhook
		if raw == nil {
			if stamp != previousStamp || cached == nil || cached.token != token.Bytes || cached.tuple != string(tuple) || len(cached.raw) != length {
				return 0, 0, plugindispatch.ErrCorrupt
			}
			checked = cached
		} else {
			if len(raw) != length {
				return 0, 0, plugindispatch.ErrCorrupt
			}
			checked, err = validateWebhookBytes(raw, cached)
			if err != nil {
				return 0, 0, err
			}
			// validateWebhookBytes may reuse an exact-byte match with an older
			// token. Never mutate that immutable entry shared with another reader.
			if checked.token != token.Bytes || checked.tuple != string(tuple) {
				owned := *checked
				owned.token = token.Bytes
				owned.tuple = string(tuple)
				checked = &owned
			}
		}
		i := checked.intent
		if i.ID != string(id) || i.JobID != string(job) || i.Slot != string(slot) || i.Version != version || i.State != string(state) {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		slots[i.JobID]++
		total++
		used += checked.budget
		size += length
		retained += length + checked.budget
		if slots[i.JobID] > plugindispatch.MaxSlotsPerTask || used > plugindispatch.MaxTotalBytes || size > plugindispatch.MaxTotalBytes || retained > 2*plugindispatch.MaxTotalBytes {
			return 0, 0, plugindispatch.ErrCorrupt
		}
		next[i.ID] = checked
		if visit != nil {
			visit(i)
		}
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return 0, 0, plugindispatch.ErrUnavailable
	}
	if stamp == "" {
		return 0, 0, plugindispatch.ErrCorrupt
	}
	memo.publish(next, stamp)
	return total, used, nil
}

func queryCurrentWebhookRows(ctx context.Context, tx pgx.Tx, ids, tokens, tuples []byte, stamp string) (pgx.Rows, error) {
	conn := tx.Conn()
	// This internal reader must stay inside the caller's open transaction.
	if conn.PgConn().TxStatus() != 'T' {
		return nil, plugindispatch.ErrUnavailable
	}
	var limit [8]byte
	binary.BigEndian.PutUint64(limit[:], uint64(plugindispatch.MaxRecords+1))
	// Built-in types and binary parameters are explicit. ExecParams reparses
	// this one statement with current parameters, avoiding a generic array
	// join plan and pgx's per-query encoding copies. It uses the same pinned
	// connection/snapshot and context as the owning pgx transaction.
	result := conn.PgConn().ExecParams(ctx, currentWebhookRows,
		[][]byte{limit[:], ids, tokens, []byte(webhookTokenFunction), []byte(stamp), tuples},
		[]uint32{pgtype.Int8OID, pgtype.TextArrayOID, pgtype.UUIDArrayOID, pgtype.TextOID, pgtype.ByteaOID, pgtype.TextArrayOID},
		[]int16{pgx.BinaryFormatCode}, []int16{pgx.BinaryFormatCode})
	return pgx.RowsFromResultReader(conn.TypeMap(), result), nil
}
