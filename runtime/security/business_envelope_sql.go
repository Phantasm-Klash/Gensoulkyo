package security

import (
	"database/sql"
	"errors"
)

const insertBusinessEnvelopeAuditSQL = `
INSERT INTO business_envelope_audits (
    session_id_hint,
    user_id,
    transport,
    endpoint,
    op_code,
    key_id,
    protocol_version,
    seq,
    nonce,
    request_timestamp_ms,
    server_timestamp_ms,
    accepted,
    error_code,
    error_reason,
    replay,
    body_hash,
    auth_tag_prefix
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
)`

type SQLBusinessEnvelopeAuditSink struct {
	db        *sql.DB
	metadata  SQLBusinessEnvelopeAuditMetadata
	insertSQL string
}

type SQLBusinessEnvelopeAuditMetadata struct {
	UserID    string
	Transport string
	Endpoint  string
}

type SQLBusinessEnvelopeAuditSinkOption func(*SQLBusinessEnvelopeAuditSink)

func NewSQLBusinessEnvelopeAuditSink(db *sql.DB, options ...SQLBusinessEnvelopeAuditSinkOption) (*SQLBusinessEnvelopeAuditSink, error) {
	if db == nil {
		return nil, errors.New("business envelope audit sink requires db")
	}
	sink := &SQLBusinessEnvelopeAuditSink{
		db:        db,
		insertSQL: insertBusinessEnvelopeAuditSQL,
	}
	for _, option := range options {
		option(sink)
	}
	return sink, nil
}

func WithSQLBusinessEnvelopeAuditMetadata(metadata SQLBusinessEnvelopeAuditMetadata) SQLBusinessEnvelopeAuditSinkOption {
	return func(sink *SQLBusinessEnvelopeAuditSink) {
		sink.metadata = metadata
	}
}

func withSQLBusinessEnvelopeAuditStatement(statement string) SQLBusinessEnvelopeAuditSinkOption {
	return func(sink *SQLBusinessEnvelopeAuditSink) {
		if statement != "" {
			sink.insertSQL = statement
		}
	}
}

func (sink *SQLBusinessEnvelopeAuditSink) RecordBusinessEnvelopeAudit(audit BusinessEnvelopeAudit) error {
	if sink == nil || sink.db == nil {
		return errors.New("business envelope audit sink is not configured")
	}
	_, err := sink.db.Exec(
		sink.insertSQL,
		audit.SessionIDHint,
		nullableString(firstNonEmpty(audit.UserID, sink.metadata.UserID)),
		firstNonEmpty(audit.Transport, sink.metadata.Transport, "unknown"),
		firstNonEmpty(audit.Endpoint, sink.metadata.Endpoint, audit.Op, "unknown"),
		firstNonEmpty(audit.Op, "unknown"),
		firstNonEmpty(audit.KeyID, "unknown"),
		firstNonEmpty(audit.Version, BusinessEnvelopeVersion),
		audit.Seq,
		audit.Nonce,
		audit.TimestampMS,
		audit.ServerTimeMS,
		audit.Accepted,
		nullableString(audit.Code),
		nullableString(audit.Reason),
		audit.Replay,
		nullableString(audit.BodyHash),
		nullableString(audit.AuthTagPrefix),
	)
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
