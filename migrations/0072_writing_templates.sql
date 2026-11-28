CREATE TABLE np_writing_template_library (
    id smallint PRIMARY KEY CHECK (id = 1),
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 8388608)
);

COMMENT ON TABLE np_writing_template_library IS
    'Versioned team writing instructions, immutable revisions and permanent mutation receipts; no generation drafts or credentials.';
