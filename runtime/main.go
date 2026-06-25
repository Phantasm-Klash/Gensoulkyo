package main

import (
	"context"
	"database/sql"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/server"
	"github.com/heroiclabs/nakama-common/runtime"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	return server.InitModule(ctx, logger, db, nk, initializer)
}

