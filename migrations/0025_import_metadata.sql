-- WeChat mappings require the source excerpt and cover fingerprint.
ALTER TABLE np_content_imports ADD COLUMN metadata_digest text NOT NULL DEFAULT ''
    CHECK ((format = 'wechat' AND metadata_digest ~ '^[0-9a-f]{64}$')
        OR (format <> 'wechat' AND metadata_digest = ''));
