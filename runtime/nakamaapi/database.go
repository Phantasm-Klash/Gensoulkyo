package nakamaapi

import (
	"database/sql"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

// NewWithDatabase builds a handler whose audit repositories and whose business
// state both live in db: the rooms, matches and battle tickets are the system
// of record, so a restarted lobby finds them again.
func NewWithDatabase(db *sql.DB, options ...Option) (*Handler, error) {
	return newWithDatabase(db, nil, nil, options...)
}

// NewWithDatabaseWithNotifier builds a durable handler whose lobby pushes go out
// through the given notifier.
//
// The notifier is a core interface rather than a runtime type so core stays free
// of the runtime module; see NakamaNotifier for the implementation that turns a
// push into a Nakama notification.
func NewWithDatabaseWithNotifier(db *sql.DB, notifier core.LobbyNotifier, options ...Option) (*Handler, error) {
	return newWithDatabase(db, nil, notifier, options...)
}

// NewWithDatabaseStores builds a handler whose audit repositories use db while
// the business state lives in the given persistence seams.
//
// The capture database the audit tests use is a test double that only answers
// the statements it was primed with, so it cannot carry the store traffic.
// Those tests pass the in-memory seams to keep their original subject — the
// audit path — independent of the store path, which the store's own tests and
// the end-to-end run against a real Postgres cover.
func NewWithDatabaseStores(
	db *sql.DB,
	rooms core.RoomStore,
	matches core.MatchStore,
	tickets core.BattleTicketStore,
	queueTickets core.QueueTicketStore,
	options ...Option,
) (*Handler, error) {
	return newWithDatabase(db, &storeSeams{
		rooms:        rooms,
		matches:      matches,
		tickets:      tickets,
		queueTickets: queueTickets,
	}, nil, options...)
}

// storeSeams carries an explicit set of persistence seams.
type storeSeams struct {
	rooms        core.RoomStore
	matches      core.MatchStore
	tickets      core.BattleTicketStore
	queueTickets core.QueueTicketStore
}

func newWithDatabase(db *sql.DB, override *storeSeams, notifier core.LobbyNotifier, options ...Option) (*Handler, error) {
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
	demRepo, err := core.NewSQLDemStore(db)
	if err != nil {
		return nil, err
	}
	// The durable stores are the system of record for lobby rooms, the match
	// lifecycle and battle tickets. Without this injection the Service falls
	// back to its process-local implementations and the state dies with the
	// process, which is exactly what goal 2 removes.
	var roomStore core.RoomStore
	var matchStore core.MatchStore
	var ticketStore core.BattleTicketStore
	var queueTicketStore core.QueueTicketStore
	if override != nil {
		roomStore = override.rooms
		matchStore = override.matches
		ticketStore = override.tickets
		queueTicketStore = override.queueTickets
	} else {
		roomStore, err = core.NewRoomStore(db)
		if err != nil {
			return nil, err
		}
		matchStore, err = core.NewMatchStore(db)
		if err != nil {
			return nil, err
		}
		ticketStore, err = core.NewBattleTicketStore(db)
		if err != nil {
			return nil, err
		}
		queueTicketStore, err = core.NewQueueTicketStore(db)
		if err != nil {
			return nil, err
		}
	}
	guard := security.NewBusinessEnvelopeGuard(security.WithBusinessEnvelopeAuditSink(sink))
	service := core.NewService(core.Config{
		BattleLifecycleAuditRepo: battleRepo,
		LobbyLifecycleAuditRepo:  lobbyRepo,
		DemRepository:            demRepo,
		Rooms:                    roomStore,
		Matches:                  matchStore,
		BattleTickets:            ticketStore,
		QueueTickets:             queueTicketStore,
		PlayableModes:            core.PlayableModeIDs(),
		Notifier:                 notifier,
	})
	handlerOptions := []Option{WithBusinessEnvelopeGuard(guard)}
	handlerOptions = append(handlerOptions, options...)
	return New(service, handlerOptions...), nil
}
