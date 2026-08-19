package rest

import (
	"encoding/json"
	"errors"
	"net/http"

	authapp "github.com/mateusxis/cassino/internal/application/auth"
	gameapp "github.com/mateusxis/cassino/internal/application/game"
	"github.com/mateusxis/cassino/internal/domain/game"
	"github.com/mateusxis/cassino/internal/domain/player"
)

// Sentinel errors raised by this package's own middleware (as opposed to the
// domain/application layers) so mapError has something typed to switch on.
var (
	// errMissingToken is returned when no (or an empty) bearer token is present.
	errMissingToken = errors.New("rest: missing bearer token")
	// errUnauthenticated is returned when a handler that requires an
	// authenticated caller is somehow reached without one — defensive, should
	// be unreachable given the middleware wiring.
	errUnauthenticated = errors.New("rest: authentication required")
	// errInvalidToken is returned when the bearer token fails verification,
	// whether because it is malformed, mis-signed, or expired. The three
	// cases are deliberately not distinguished in the client-facing response:
	// all of them mean "log in again."
	errInvalidToken = errors.New("rest: invalid or expired token")
	// errMalformedBody is returned when the request body is not valid JSON
	// for the endpoint's expected shape.
	errMalformedBody = errors.New("rest: request body is malformed")
)

// errBody is the "error" object inside every non-2xx response.
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// envelope is the top-level shape of an error response:
// {"error": {"code": "...", "message": "..."}}
type envelope struct {
	Error *errBody `json:"error"`
}

// mapError translates a domain/application/transport error into an HTTP
// status code plus a stable machine-readable code and a client-safe message.
// Error-code mapping used across the whole REST surface:
//
//	invalid input (bad email, weak password, non-positive amount, bad JSON) -> 400 invalid_request
//	bad credentials / missing / invalid / expired token                     -> 401 (invalid_credentials | unauthorized)
//	duplicate email                                                          -> 409 email_already_used
//	insufficient balance                                                     -> 409 insufficient_balance
//	withdrawal blocked (active room)                                         -> 409 withdrawal_blocked
//	room conflicts (full, closed, already joined, round in progress, ...)     -> 409 <specific code>
//	owner-only action attempted by another player                            -> 403 not_owner
//	unknown player/resource                                                  -> 404 not_found
//	anything else                                                            -> 500 internal_error
//
// Room and round conflicts are 409 rather than 422 for the same reason the
// wallet conflicts are: the request itself is well formed, it just lost a race
// with the state of the table.
func mapError(err error) (status int, code string, message string) {
	switch {
	case errors.Is(err, player.ErrInvalidEmail):
		return http.StatusBadRequest, "invalid_request", "the e-mail address is invalid"
	case errors.Is(err, authapp.ErrPasswordTooShort):
		return http.StatusBadRequest, "invalid_request", "the password is too short"
	case errors.Is(err, player.ErrInvalidAmount):
		return http.StatusBadRequest, "invalid_request", "amount must be greater than zero"
	case errors.Is(err, player.ErrInvalidID):
		return http.StatusBadRequest, "invalid_request", "invalid identifier"
	case errors.Is(err, errMalformedBody):
		return http.StatusBadRequest, "invalid_request", "the request body is malformed"
	case errors.Is(err, game.ErrInvalidChoice):
		return http.StatusBadRequest, "invalid_request", "choice must be even or odd"
	case errors.Is(err, game.ErrInvalidAmount):
		return http.StatusBadRequest, "invalid_request", "amount must be greater than zero"
	case errors.Is(err, game.ErrInvalidID), errors.Is(err, game.ErrInvalidRoundNumber):
		return http.StatusBadRequest, "invalid_request", "invalid identifier"

	case errors.Is(err, player.ErrInvalidCredentials):
		return http.StatusUnauthorized, "invalid_credentials", "invalid e-mail or password"
	case errors.Is(err, errMissingToken), errors.Is(err, errInvalidToken), errors.Is(err, errUnauthenticated):
		return http.StatusUnauthorized, "unauthorized", "authentication is required"

	case errors.Is(err, player.ErrEmailAlreadyUsed):
		return http.StatusConflict, "email_already_used", "an account with this e-mail already exists"
	case errors.Is(err, player.ErrInsufficientBalance):
		return http.StatusConflict, "insufficient_balance", "balance is insufficient for this operation"
	case errors.Is(err, player.ErrWithdrawalBlocked):
		return http.StatusConflict, "withdrawal_blocked", "withdrawals are blocked while you are in an active room"

	case errors.Is(err, game.ErrNotOwner):
		return http.StatusForbidden, "not_owner", "only the room owner may perform this action"
	case errors.Is(err, game.ErrNotAMember):
		return http.StatusForbidden, "not_a_member", "you are not a member of this room"

	case errors.Is(err, game.ErrRoomFull):
		return http.StatusConflict, "room_full", "the room is full"
	case errors.Is(err, game.ErrRoomClosed):
		return http.StatusConflict, "room_closed", "the room is closed"
	case errors.Is(err, game.ErrRoomNotOpen):
		return http.StatusConflict, "room_not_open", "the room is no longer accepting players"
	case errors.Is(err, game.ErrAlreadyJoined):
		return http.StatusConflict, "already_joined", "you already joined this room"
	case errors.Is(err, game.ErrAlreadyInAnotherRoom):
		return http.StatusConflict, "already_in_another_room", "you are already in another room"
	case errors.Is(err, game.ErrRoundInProgress):
		return http.StatusConflict, "round_in_progress", "a round is already in progress"
	case errors.Is(err, game.ErrNoActiveRound):
		return http.StatusConflict, "no_active_round", "there is no round accepting bets"
	case errors.Is(err, game.ErrRoundAlreadySettled):
		return http.StatusConflict, "round_settled", "the round is already settled"
	case errors.Is(err, game.ErrMaxRoundsReached):
		return http.StatusConflict, "max_rounds_reached", "the room played all of its rounds"
	case errors.Is(err, game.ErrNoPlayers):
		return http.StatusConflict, "no_players", "the room has no players"
	case errors.Is(err, game.ErrBettingClosed), errors.Is(err, game.ErrBettingNotOpen):
		return http.StatusConflict, "betting_closed", "the betting window is closed"
	case errors.Is(err, game.ErrDuplicateBet):
		return http.StatusConflict, "duplicate_bet", "you already placed a bet in this round"
	case errors.Is(err, gameapp.ErrJoinInProgress):
		return http.StatusConflict, "join_in_progress", "another join is in progress, retry"
	case errors.Is(err, gameapp.ErrEngineStopped):
		return http.StatusServiceUnavailable, "server_shutting_down", "the server is shutting down"

	case errors.Is(err, game.ErrRoomNotFound):
		return http.StatusNotFound, "room_not_found", "room not found"
	case errors.Is(err, player.ErrNotFound), errors.Is(err, game.ErrRoundNotFound), errors.Is(err, game.ErrBetNotFound):
		return http.StatusNotFound, "not_found", "resource not found"

	default:
		return http.StatusInternalServerError, "internal_error", "an unexpected error occurred"
	}
}

// writeJSON encodes v as the response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError renders the standard error envelope for err and records the
// underlying error text on the request's audit recorder, if any, so the
// audit log carries the real failure even though the client only sees the
// curated, non-leaky message.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := mapError(err)
	if ar := auditRecorderFrom(r.Context()); ar != nil {
		ar.SetError(err.Error())
	}
	writeJSON(w, status, envelope{Error: &errBody{Code: code, Message: message}})
}

// maxRequestBodyBytes bounds every request body this API accepts. It is
// generous for the small JSON payloads used here and mainly a defence
// against a client streaming an unbounded body at us.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// decodeJSON reads and decodes a JSON request body, rejecting anything that
// is not valid JSON for dst's shape or that carries unknown fields.
func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errMalformedBody
	}
	return nil
}
