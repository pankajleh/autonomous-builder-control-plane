CREATE TABLE public.workflow_authority_v1 (
    controller_identity char(64) PRIMARY KEY,
    authority_domain char(64) NOT NULL,
    repository_identity text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    canonical_state bytea NOT NULL CHECK (
        octet_length(canonical_state) > 0
        AND octet_length(canonical_state) <= 1048576
    ),
    state_sha256 char(64) NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (controller_identity ~ '^[0-9a-f]{64}$'),
    CHECK (authority_domain ~ '^[0-9a-f]{64}$'),
    CHECK (octet_length(repository_identity) > 0 AND octet_length(repository_identity) <= 4096),
    CHECK (state_sha256 ~ '^[0-9a-f]{64}$')
);
