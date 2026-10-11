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
	guard := security.NewBusinessEnvelopeGuard(security.WithBusinessEnvelopeAuditSink(sink))
	handlerOptions := append([]Option{WithBusinessEnvelopeGuard(guard)}, options...)

	// Resolve the options against a throwaway handler first: a service-level
	// option (the account subsystem) has to be folded into core.Config, which
	// must happen before the Service is constructed. NewWithOptions then applies
	// the same options to the real handler.
	probe := &Handler{}
	for _, option := range handlerOptions {
		option(probe)
	}
	service := core.NewService(core.Config{
		BattleLifecycleAuditRepo: battleRepo,
		LobbyLifecycleAuditRepo:  lobbyRepo,
		Accounts:                 probe.serviceAccounts,
	})
	return HandlerWithDatabase{Service: service, Handler: NewWithOptions(service, handlerOptions...)}, nil
}

type HandlerWithDatabase struct {
	Service *core.Service
	Handler http.Handler
}
