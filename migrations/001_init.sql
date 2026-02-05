CREATE TABLE IF NOT EXISTS adapter_event_state (
    event_id UUID PRIMARY KEY,
    attempts INT NOT NULL,
    status TEXT NOT NULL,
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS adapter_outbox (
    id UUID PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);
