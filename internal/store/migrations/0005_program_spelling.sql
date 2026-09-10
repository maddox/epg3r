-- The placeholder setting's key lost its British spelling along with the rest of the code.
-- Renamed rather than left behind, so an install that turned it on keeps it on: the app
-- reads the new key and would otherwise fall back to the default and quietly switch it off.
UPDATE settings SET key = 'emit_placeholder_program' WHERE key = 'emit_placeholder_programme';
