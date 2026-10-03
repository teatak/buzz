-- Run once on the copied, dedicated Redis database before starting Buzz.
-- Keep values, types, and TTLs. Refuse collisions before changing any key.
local keys = redis.call('KEYS', 'bh:*')
for _, key in ipairs(keys) do
  if redis.call('EXISTS', 'buzz:' .. string.sub(key, 4)) == 1 then
    return redis.error_reply('Buzz migration destination key already exists; no keys changed')
  end
end
for _, key in ipairs(keys) do
  redis.call('RENAME', key, 'buzz:' .. string.sub(key, 4))
end
return #keys
