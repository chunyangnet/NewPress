-- References store identity/version; public output rechecks current sharing.
CREATE TABLE np_snippets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    version bigint NOT NULL CHECK (version > 0),
    visibility text NOT NULL CHECK (visibility IN ('private','shared','withdrawn')),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 4096)
);
CREATE TABLE np_snippet_revisions (
    snippet_id bigint NOT NULL REFERENCES np_snippets(id),
    version bigint NOT NULL CHECK (version > 0),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 2097152),
    PRIMARY KEY (snippet_id,version)
);
CREATE TABLE np_snippet_receipts (
    identity text NOT NULL,
    request_key text NOT NULL CHECK (request_key ~ '^[0-9a-f]{64}$'),
    snippet_id bigint NOT NULL REFERENCES np_snippets(id),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 16384),
    PRIMARY KEY (identity,request_key)
);
