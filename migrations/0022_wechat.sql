CREATE TABLE np_wechat_state (
    id integer PRIMARY KEY CHECK (id = 1),
    data jsonb NOT NULL CHECK (jsonb_typeof(data) = 'object' AND octet_length(data::text) <= 16777216)
);
INSERT INTO np_wechat_state(id,data) VALUES(1,'{"schema":1,"config":{"version":1,"collect_enabled":true,"include_source":true},"jobs":[]}');
ALTER TABLE np_content_imports DROP CONSTRAINT np_content_imports_format_check;
ALTER TABLE np_content_imports ADD CONSTRAINT np_content_imports_format_check CHECK (format IN ('markdown','wordpress','wechat'));
