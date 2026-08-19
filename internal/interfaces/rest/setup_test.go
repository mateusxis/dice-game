package rest_test

import (
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
	"github.com/mateusxis/cassino/internal/interfaces/rest"
)

// testDeps bundles the fakes backing a router built by newTestRouter, so
// tests can seed state or inspect what was recorded.
type testDeps struct {
	players      *fakePlayerRepo
	transactions *fakeTransactionRepo
	rooms        *fakeRoomRepo
	sessions     *fakeSessionStore
	ids          *fakeIDGen
	tokens       *fakeTokenService
	auditRepo    *fakeAuditRepo
}

// newTestRouter wires a full router — auth, wallet, JWT middleware, audit
// middleware — entirely against in-memory fakes; no network or database I/O
// happens anywhere in these tests.
func newTestRouter(t *testing.T) (*chi.Mux, *testDeps) {
	t.Helper()

	deps := &testDeps{
		players:      newFakePlayerRepo(),
		transactions: &fakeTransactionRepo{},
		rooms:        &fakeRoomRepo{},
		sessions:     &fakeSessionStore{},
		ids:          &fakeIDGen{},
		tokens:       newFakeTokenService(),
		auditRepo:    &fakeAuditRepo{},
	}

	clk := fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	hasher := fakeHasher{}
	tx := fakeTxManager{}

	registerUC := authapp.NewRegisterUseCase(deps.players, hasher, deps.ids, clk)
	loginUC := authapp.NewLoginUseCase(deps.players, hasher, deps.tokens)
	depositUC := walletapp.NewDepositUseCase(tx, deps.players, deps.transactions, deps.ids, clk)
	withdrawUC := walletapp.NewWithdrawUseCase(tx, deps.players, deps.transactions, deps.rooms, deps.sessions, deps.ids, clk)
	balanceUC := walletapp.NewGetBalanceUseCase(deps.players)

	router := rest.NewRouter(rest.RouterOptions{
		Version:         "test",
		RegisterUseCase: registerUC,
		LoginUseCase:    loginUC,
		DepositUseCase:  depositUC,
		WithdrawUseCase: withdrawUC,
		BalanceUseCase:  balanceUC,
		Tokens:          deps.tokens,
		AuditRepo:       deps.auditRepo,
		Clock:           clk,
		IDs:             deps.ids,
	})

	return router, deps
}
