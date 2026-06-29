package nakamaapi

import (
	"database/sql"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

func NewWithDatabase(db *sql.DB, options ...Option) (*Handler, error) {
	sink, err := security.NewSQLBusinessEnvelopeAuditSink(db)
	if err != nil {
		return nil, err
	}
	battleRepo, err := core.NewSQLBattleLifecycleAuditRepository(db)
	if err != nil {
		return nil, err
	}
	lobbyRepo, err := core.NewSQLLobbyLifecycleAuditRepository(db)
	if err != nil {
		return nil, err
	}
	guard := security.NewBusinessEnvelopeGuard(security.WithBusinessEnvelopeAuditSink(sink))
	service := core.NewService(core.Config{
		BattleLifecycleAuditRepo: battleRepo,
		LobbyLifecycleAuditRepo:  lobbyRepo,
	})
	handlerOptions := []Option{WithBusinessEnvelopeGuard(guard)}
	handlerOptions = append(handlerOptions, options...)
	return New(service, handlerOptions...), nil
}
