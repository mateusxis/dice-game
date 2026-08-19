-- Initial schema for the dice betting engine.
-- Money is BIGINT cents everywhere; all timestamps are timestamptz (UTC).

-- gen_random_uuid() is built into PostgreSQL 13+; pgcrypto is created anyway so
-- the schema also applies on older servers and gives us crypt()/digest() if a
-- later migration needs them.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ---------------------------------------------------------------------------
-- players
-- ---------------------------------------------------------------------------
CREATE TABLE players (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        NOT NULL,
    password_hash TEXT        NOT NULL,
    balance       BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT players_balance_non_negative CHECK (balance >= 0),
    CONSTRAINT players_email_not_blank      CHECK (length(btrim(email)) > 0)
);

-- Case-insensitive uniqueness: the application normalizes to lowercase, the
-- index guarantees it even if some path forgets.
CREATE UNIQUE INDEX players_email_key ON players (lower(email));

-- ---------------------------------------------------------------------------
-- rooms
-- ---------------------------------------------------------------------------
CREATE TABLE rooms (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id      UUID        NOT NULL REFERENCES players (id) ON DELETE RESTRICT,
    status        TEXT        NOT NULL DEFAULT 'open',
    max_rounds    INT         NOT NULL DEFAULT 10,
    max_players   INT         NOT NULL DEFAULT 6,
    current_round INT         NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at     TIMESTAMPTZ,
    CONSTRAINT rooms_status_valid   CHECK (status IN ('open', 'closing', 'closed')),
    CONSTRAINT rooms_max_rounds_pos CHECK (max_rounds > 0),
    CONSTRAINT rooms_max_players_pos CHECK (max_players > 0),
    CONSTRAINT rooms_round_range    CHECK (current_round >= 0 AND current_round <= max_rounds),
    CONSTRAINT rooms_closed_at_set  CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

CREATE INDEX rooms_open_idx ON rooms (created_at DESC) WHERE status = 'open';
CREATE INDEX rooms_owner_idx ON rooms (owner_id);

-- ---------------------------------------------------------------------------
-- room_players (seat set, max 6 enforced in the application + a Redis lock)
-- ---------------------------------------------------------------------------
CREATE TABLE room_players (
    room_id   UUID        NOT NULL REFERENCES rooms (id)   ON DELETE CASCADE,
    player_id UUID        NOT NULL REFERENCES players (id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, player_id)
);

CREATE INDEX room_players_player_idx ON room_players (player_id);

-- ---------------------------------------------------------------------------
-- rounds
-- ---------------------------------------------------------------------------
CREATE TABLE rounds (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    room_id           UUID        NOT NULL REFERENCES rooms (id) ON DELETE CASCADE,
    number            INT         NOT NULL,
    betting_opens_at  TIMESTAMPTZ NOT NULL,
    betting_closes_at TIMESTAMPTZ NOT NULL,
    die1              SMALLINT,
    die2              SMALLINT,
    outcome           TEXT,
    status            TEXT        NOT NULL DEFAULT 'betting',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at        TIMESTAMPTZ,
    CONSTRAINT rounds_number_positive CHECK (number >= 1),
    CONSTRAINT rounds_status_valid    CHECK (status IN ('betting', 'settled')),
    CONSTRAINT rounds_outcome_valid   CHECK (outcome IS NULL OR outcome IN ('even', 'odd')),
    CONSTRAINT rounds_die1_range      CHECK (die1 IS NULL OR (die1 BETWEEN 1 AND 6)),
    CONSTRAINT rounds_die2_range      CHECK (die2 IS NULL OR (die2 BETWEEN 1 AND 6)),
    CONSTRAINT rounds_window_valid    CHECK (betting_closes_at > betting_opens_at),
    -- A settled round must carry a full result; an unsettled one must not.
    CONSTRAINT rounds_settled_complete CHECK (
        (status = 'settled' AND die1 IS NOT NULL AND die2 IS NOT NULL AND outcome IS NOT NULL AND settled_at IS NOT NULL)
        OR
        (status = 'betting' AND die1 IS NULL AND die2 IS NULL AND outcome IS NULL AND settled_at IS NULL)
    ),
    CONSTRAINT rounds_room_number_key UNIQUE (room_id, number)
);

CREATE INDEX rounds_room_idx ON rounds (room_id, number DESC);

-- ---------------------------------------------------------------------------
-- bets — one per (round, player); the unique constraint is the real defence
-- against concurrent duplicate submissions.
-- ---------------------------------------------------------------------------
CREATE TABLE bets (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    round_id   UUID        NOT NULL REFERENCES rounds (id)  ON DELETE CASCADE,
    player_id  UUID        NOT NULL REFERENCES players (id) ON DELETE RESTRICT,
    choice     TEXT        NOT NULL,
    amount     BIGINT      NOT NULL,
    won        BOOLEAN,
    payout     BIGINT,
    placed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at TIMESTAMPTZ,
    CONSTRAINT bets_choice_valid   CHECK (choice IN ('even', 'odd')),
    CONSTRAINT bets_amount_positive CHECK (amount > 0),
    CONSTRAINT bets_payout_non_negative CHECK (payout IS NULL OR payout >= 0),
    CONSTRAINT bets_settlement_consistent CHECK ((won IS NULL) = (payout IS NULL)),
    CONSTRAINT bets_round_player_key UNIQUE (round_id, player_id)
);

CREATE INDEX bets_player_idx ON bets (player_id);
CREATE INDEX bets_round_idx  ON bets (round_id);

-- ---------------------------------------------------------------------------
-- transactions — append-only wallet ledger. The sum of a player's entries
-- (deposit + payout - withdraw - bet) always equals players.balance.
-- ---------------------------------------------------------------------------
CREATE TABLE transactions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id  UUID        NOT NULL REFERENCES players (id) ON DELETE RESTRICT,
    type       TEXT        NOT NULL,
    amount     BIGINT      NOT NULL,
    round_id   UUID        REFERENCES rounds (id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT transactions_type_valid    CHECK (type IN ('deposit', 'withdraw', 'bet', 'payout')),
    CONSTRAINT transactions_amount_positive CHECK (amount > 0)
);

CREATE INDEX transactions_player_idx ON transactions (player_id, created_at DESC);
CREATE INDEX transactions_round_idx  ON transactions (round_id);

-- ---------------------------------------------------------------------------
-- audit_logs — read-only trail. INSERT is allowed; UPDATE and DELETE raise.
-- ---------------------------------------------------------------------------
CREATE TABLE audit_logs (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    occurred_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_id          UUID        REFERENCES players (id) ON DELETE SET NULL,
    channel           TEXT        NOT NULL,
    endpoint_or_event TEXT        NOT NULL,
    http_method       TEXT,
    action            TEXT        NOT NULL,
    error             TEXT,
    payload           JSONB,
    CONSTRAINT audit_logs_channel_valid CHECK (channel IN ('rest', 'ws')),
    CONSTRAINT audit_logs_endpoint_not_blank CHECK (length(btrim(endpoint_or_event)) > 0),
    CONSTRAINT audit_logs_action_not_blank CHECK (length(btrim(action)) > 0)
);

CREATE INDEX audit_logs_occurred_idx ON audit_logs (occurred_at DESC);
CREATE INDEX audit_logs_actor_idx    ON audit_logs (actor_id, occurred_at DESC);

CREATE OR REPLACE FUNCTION audit_logs_deny_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only: % is not permitted', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER audit_logs_no_update
    BEFORE UPDATE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_deny_mutation();

CREATE TRIGGER audit_logs_no_delete
    BEFORE DELETE ON audit_logs
    FOR EACH ROW EXECUTE FUNCTION audit_logs_deny_mutation();

-- TRUNCATE bypasses row-level triggers, so it gets a statement-level one.
CREATE TRIGGER audit_logs_no_truncate
    BEFORE TRUNCATE ON audit_logs
    FOR EACH STATEMENT EXECUTE FUNCTION audit_logs_deny_mutation();
