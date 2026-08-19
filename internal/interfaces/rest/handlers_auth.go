package rest

import (
	"net/http"
	"time"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
)

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type registerResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

// RegisterHandler handles POST /auth/register. It never echoes the password
// or its hash back to the caller.
func RegisterHandler(uc *authapp.RegisterUseCase) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, r, err)
			return
		}

		out, err := uc.Execute(r.Context(), authapp.RegisterInput{Email: req.Email, Password: req.Password})
		if err != nil {
			writeError(w, r, err)
			return
		}

		writeJSON(w, http.StatusCreated, registerResponse{
			ID:        out.PlayerID,
			Email:     out.Email,
			Balance:   0,
			CreatedAt: out.CreatedAt,
		})
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// LoginHandler handles POST /auth/login.
func LoginHandler(uc *authapp.LoginUseCase) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, r, err)
			return
		}

		out, err := uc.Execute(r.Context(), authapp.LoginInput{Email: req.Email, Password: req.Password})
		if err != nil {
			writeError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, loginResponse{Token: out.Token, ExpiresAt: out.ExpiresAt})
	}
}
