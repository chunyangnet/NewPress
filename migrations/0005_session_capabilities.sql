UPDATE np_roles
SET capabilities = '["admin:access","dashboard:read","content:read","content:write","taxonomy:manage","media:read","media:write","comments:moderate","system:read","sessions:read","sessions:manage:own"]'::jsonb
WHERE slug = 'editor';

UPDATE np_roles
SET capabilities = '["admin:access","dashboard:read","content:read","content:write:own","media:read","profile:write:own","sessions:read","sessions:manage:own"]'::jsonb
WHERE slug = 'author';
