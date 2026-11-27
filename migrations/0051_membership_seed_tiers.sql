-- Keep new PostgreSQL installations aligned with the file-store defaults.
-- A missing tier historically means tier 1. Do not elevate existing members
-- or overwrite a configured plan: only repair the unused original seed.
UPDATE np_progress_settings
SET data = jsonb_set(jsonb_set(data, '{membership,plans,0,tier}', '1'), '{membership,plans,1,tier}', '2')
WHERE id = 1
  AND data->>'version' = '1'
  AND data->'membership' = '{
    "enabled":false,"public_badges":true,"plans":[
      {"key":"member","name":"标准会员","description":"会员徽章与经验加成","color":"#b57525","enabled":true,"days":30,"experience_percent":125,"purchase_enabled":false,"currency":"main","price":"100"},
      {"key":"supporter","name":"年度会员","description":"更长的会员有效期与经验加成","color":"#287456","enabled":true,"days":365,"experience_percent":150,"purchase_enabled":false,"currency":"main","price":"1000"}
    ]
  }'::jsonb
  AND NOT EXISTS (SELECT 1 FROM np_progress_accounts)
  AND NOT EXISTS (SELECT 1 FROM np_progress_orders);
