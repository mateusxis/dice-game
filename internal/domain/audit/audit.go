// Package audit defines the append-only audit trail entity. Every REST call
// and WebSocket event produces exactly one entry. The table backing it rejects
// UPDATE and DELETE at the database level, so entries are immutable once
// written — the entity mirrors that by exposing no mutators.
package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Errors raised while building an audit entry.
var (
	// ErrInvalidChannel is returned for a channel other than rest/ws.
	ErrInvalidChannel = errors.New("audit: channel must be rest or ws")
	// ErrInvalidEndpoint is returned when the endpoint/event name is empty.
	ErrInvalidEndpoint = errors.New("audit: endpoint or event is required")
	// ErrInvalidAction is returned when the action name is empty.
	ErrInvalidAction = errors.New("audit: action is required")
	// ErrImmutable is returned by repositories that reject a mutation attempt.
	ErrImmutable = errors.New("audit: entries are immutable")
)

// Channel identifies the transport that produced the entry.
type Channel string

const (
	// ChannelREST marks an HTTP request.
	ChannelREST Channel = "rest"
	// ChannelWS marks a WebSocket event.
	ChannelWS Channel = "ws"
)

// Valid reports whether c is a known channel.
func (c Channel) Valid() bool { return c == ChannelREST || c == ChannelWS }

// String implements fmt.Stringer.
func (c Channel) String() string { return string(c) }

// Entry is one immutable audit record.
type Entry struct {
	ID         string
	OccurredAt time.Time
	// ActorID is the authenticated player id, nil for anonymous calls such as
	// register and login failures.
	ActorID *string
	Channel Channel
	// EndpointOrEvent is the HTTP route pattern ("/wallet/deposit") or the
	// WebSocket event name ("bet.place").
	EndpointOrEvent string
	// HTTPMethod is set for REST entries only.
	HTTPMethod *string
	// Action is the business operation attempted ("wallet.deposit").
	Action string
	// Error carries the failure message when the operation did not succeed.
	Error *string
	// Payload is the request/event body as JSON, with secrets already
	// redacted by the caller. Nil when there is no body.
	Payload json.RawMessage
}

// NewEntry builds a validated audit entry.
func NewEntry(id string, occurredAt time.Time, actorID *string, channel Channel, endpointOrEvent, action string, httpMethod *string, opErr error, payload json.RawMessage) (*Entry, error) {
	if !channel.Valid() {
		return nil, ErrInvalidChannel
	}
	if strings.TrimSpace(endpointOrEvent) == "" {
		return nil, ErrInvalidEndpoint
	}
	if strings.TrimSpace(action) == "" {
		return nil, ErrInvalidAction
	}
	var errMsg *string
	if opErr != nil {
		m := opErr.Error()
		errMsg = &m
	}
	return &Entry{
		ID:              id,
		OccurredAt:      occurredAt.UTC(),
		ActorID:         actorID,
		Channel:         channel,
		EndpointOrEvent: endpointOrEvent,
		HTTPMethod:      httpMethod,
		Action:          action,
		Error:           errMsg,
		Payload:         payload,
	}, nil
}

// Succeeded reports whether the audited operation completed without error.
func (e *Entry) Succeeded() bool { return e.Error == nil }
