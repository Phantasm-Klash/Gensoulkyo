package httpapi

import (
	"database/sql"
	"net/http"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

func NewWithDatabase(db *sql.DB, options ...Option) (HandlerWithDatabase, error) {
	sink, err := security.NewSQLBusinessEnvelopeAuditSink(db)
	if err != nil {
		return HandlerWithDatabase{}, err
	}
	battleRepo, err := core.NewSQLBattleLifecycleAuditRepository(db)
	if err != nil {
		return HandlerWithDatabase{}, err
	}
	lobbyRepo, err := core.NewSQLLobbyLifecycleAuditRepository(db)
	if err != nil {
		return HandlerWithDatabase{}, err
	}
	service := core.NewService(core.Config{
		BattleLifecycleAuditRepo: battleRepo,
		LobbyLifecycleAuditRepo:  lobbyRepo,
	})
	guard := security.NewBusinessEnvelopeGuard(security.WithBusinessEnvelopeAuditSink(sink))
	handlerOptions := []Option{WithBusinessEnvelopeGuard(guard)}
	handlerOptions = append(handlerOptions, options...)
	return HandlerWithDatabase{Service: service, Handler: NewWithOptions(service, handlerOptions...)}, nil
}

type HandlerWithDatabase struct {
	Service *core.Service
	Handler http.Handler
}
