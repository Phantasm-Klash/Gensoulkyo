package security

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestSQLBusinessEnvelopeAuditSinkRecordsAudit(t *testing.T) {
	driverName := registerAuditCaptureDriver(t)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	sink, err := NewSQLBusinessEnvelopeAuditSink(db)
	if err != nil {
		t.Fatal(err)
	}
	audit := BusinessEnvelopeAudit{
		SessionIDHint: "session:1234567890abcdef",
		UserID:        "user-a",
		Transport:     "http_fallback",
		Endpoint:      "/v1/bootstrap",
		Op:            "bootstrap",
		KeyID:         "dev-business-envelope-v0",
		Version:       BusinessEnvelopeVersion,
		Seq:           7,
		Nonce:         "nonce-a",
		TimestampMS:   1782552000000,
		ServerTimeMS:  1782552000100,
		Accepted:      false,
		Code:          CodeBusinessEnvelopeReplay,
		Reason:        ReasonSeqReplay,
		Replay:        true,
		BodyHash:      "body-hash",
		AuthTagPrefix: "0123456789abcdef",
	}

	if err := sink.RecordBusinessEnvelopeAudit(audit); err != nil {
		t.Fatal(err)
	}
	calls := auditCaptureCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one insert, got %+v", calls)
	}
	call := calls[0]
	if !strings.Contains(call.query, "INSERT INTO business_envelope_audits") || len(call.args) != 17 {
		t.Fatalf("unexpected insert call: %+v", call)
	}
	if call.args[0] != audit.SessionIDHint || call.args[2] != "http_fallback" || call.args[4] != "bootstrap" || call.args[7] != int64(7) || call.args[14] != true {
		t.Fatalf("audit insert args mismatch: %+v", call.args)
	}
}

func TestSQLBusinessEnvelopeAuditSinkRejectsNilDB(t *testing.T) {
	if _, err := NewSQLBusinessEnvelopeAuditSink(nil); err == nil {
		t.Fatalf("expected nil db rejection")
	}
	var sink *SQLBusinessEnvelopeAuditSink
	if err := sink.RecordBusinessEnvelopeAudit(BusinessEnvelopeAudit{}); err == nil {
		t.Fatalf("expected nil sink rejection")
	}
}

type auditCaptureCall struct {
	query string
	args  []any
}

var auditCaptureState = struct {
	sync.Mutex
	nextID int
	calls  []auditCaptureCall
}{}

func registerAuditCaptureDriver(t *testing.T) string {
	t.Helper()
	auditCaptureState.Lock()
	defer auditCaptureState.Unlock()
	auditCaptureState.nextID++
	auditCaptureState.calls = nil
	name := "audit_capture_driver_" + string(rune('a'+auditCaptureState.nextID))
	sql.Register(name, auditCaptureDriver{})
	return name
}

func auditCaptureCalls() []auditCaptureCall {
	auditCaptureState.Lock()
	defer auditCaptureState.Unlock()
	calls := make([]auditCaptureCall, len(auditCaptureState.calls))
	copy(calls, auditCaptureState.calls)
	return calls
}

type auditCaptureDriver struct{}

func (auditCaptureDriver) Open(name string) (driver.Conn, error) {
	return auditCaptureConn{}, nil
}

type auditCaptureConn struct{}

func (auditCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return auditCaptureStmt{query: query}, nil
}

func (auditCaptureConn) Close() error {
	return nil
}

func (auditCaptureConn) Begin() (driver.Tx, error) {
	return auditCaptureTx{}, nil
}

func (auditCaptureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	auditCaptureState.Lock()
	auditCaptureState.calls = append(auditCaptureState.calls, auditCaptureCall{query: query, args: values})
	auditCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (auditCaptureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return auditCaptureRows{}, nil
}

type auditCaptureStmt struct {
	query string
}

func (stmt auditCaptureStmt) Close() error {
	return nil
}

func (stmt auditCaptureStmt) NumInput() int {
	return -1
}

func (stmt auditCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	auditCaptureState.Lock()
	auditCaptureState.calls = append(auditCaptureState.calls, auditCaptureCall{query: stmt.query, args: values})
	auditCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (stmt auditCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	return auditCaptureRows{}, nil
}

type auditCaptureTx struct{}

func (auditCaptureTx) Commit() error {
	return nil
}

func (auditCaptureTx) Rollback() error {
	return nil
}

type auditCaptureRows struct{}

func (auditCaptureRows) Columns() []string {
	return nil
}

func (auditCaptureRows) Close() error {
	return nil
}

func (auditCaptureRows) Next(dest []driver.Value) error {
	return io.EOF
}
