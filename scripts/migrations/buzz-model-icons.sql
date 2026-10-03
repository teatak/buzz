-- Run once on the restored database before starting Buzz.
BEGIN;
UPDATE models SET icon = 'buzz' WHERE icon = 'buzzhive';
COMMIT;
