ALTER TABLE np_currency_settings
 ADD COLUMN check_in_mode text NOT NULL DEFAULT 'interval' CHECK (check_in_mode IN ('interval','calendar_day')),
 ADD COLUMN check_in_timezone text NOT NULL DEFAULT 'Asia/Shanghai';
