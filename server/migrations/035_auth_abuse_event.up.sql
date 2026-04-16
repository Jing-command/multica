CREATE TABLE auth_abuse_event (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type TEXT NOT NULL,
    identifier TEXT NOT NULL,
    ip TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_auth_abuse_event_type_created_at
    ON auth_abuse_event(event_type, created_at);

CREATE INDEX idx_auth_abuse_event_identifier_type_created_at
    ON auth_abuse_event(identifier, event_type, created_at);

CREATE INDEX idx_auth_abuse_event_ip_type_created_at
    ON auth_abuse_event(ip, event_type, created_at);

CREATE INDEX idx_auth_abuse_event_ip_identifier_type_created_at
    ON auth_abuse_event(ip, identifier, event_type, created_at);

CREATE INDEX idx_auth_abuse_event_created_at
    ON auth_abuse_event(created_at);
