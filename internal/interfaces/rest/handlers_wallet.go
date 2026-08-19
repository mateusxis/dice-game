package rest

import (
	"net/http"

	walletapp "github.com/mateusxis/cassino/internal/application/wallet"
)

type amountRequest struct {
	Amount int64 `json:"amount"`
}

type balanceResponse struct {
	Balance int64 `json:"balance"`
}

// DepositHandler handles POST /wallet/deposit. Deposits are allowed at any
// time, including while the caller is in an active room.
func DepositHandler(uc *walletapp.DepositUseCase) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, r, errUnauthenticated)
			return
		}

		var req amountRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, r, err)
			return
		}

		out, err := uc.Execute(r.Context(), walletapp.DepositInput{PlayerID: claims.PlayerID, Amount: req.Amount})
		if err != nil {
			writeError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, balanceResponse{Balance: out.Balance})
	}
}

// WithdrawHandler handles POST /wallet/withdraw. It is blocked with 409
// withdrawal_blocked while the caller is seated in an active room.
func WithdrawHandler(uc *walletapp.WithdrawUseCase) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, r, errUnauthenticated)
			return
		}

		var req amountRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, r, err)
			return
		}

		out, err := uc.Execute(r.Context(), walletapp.WithdrawInput{PlayerID: claims.PlayerID, Amount: req.Amount})
		if err != nil {
			writeError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, balanceResponse{Balance: out.Balance})
	}
}

// BalanceHandler handles GET /wallet/balance.
func BalanceHandler(uc *walletapp.GetBalanceUseCase) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok {
			writeError(w, r, errUnauthenticated)
			return
		}

		out, err := uc.Execute(r.Context(), claims.PlayerID)
		if err != nil {
			writeError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, balanceResponse{Balance: out.Balance})
	}
}
