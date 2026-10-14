package cache

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"
)

var rotateTokenScript = redis.NewScript(`
local cached = redis.call('GET', KEYS[3])
if cached then
  local result = cjson.decode(cached)
  if result.requestID == ARGV[2] and result.userID == ARGV[1] then
    return {'ok', cached}
  end
  return {'revoked', ''}
end
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return {'revoked', ''} end
if redis.call('EXISTS', KEYS[2]) ~= 0 then
  return redis.error_reply('new token key already exists')
end
local tokenTTL = tonumber(ARGV[3])
local resultTTL = tonumber(ARGV[4])
if not tokenTTL or tokenTTL <= 0 or not resultTTL or resultTTL <= 0 then
  return redis.error_reply('positive TTL required')
end
-- Lua does not roll back writes on a command error. Keep the old token until
-- both staged writes succeed, and remove staged keys on any later failure.
local written = redis.pcall('SET', KEYS[2], ARGV[1], 'PX', tokenTTL, 'NX')
if type(written) == 'table' and written.err then return written end
if not written then return redis.error_reply('new token key already exists') end
local saved = redis.pcall('SET', KEYS[3], ARGV[5], 'PX', resultTTL, 'NX')
if (type(saved) == 'table' and saved.err) or not saved then
  redis.call('DEL', KEYS[2])
  if not saved then return redis.error_reply('refresh result key already exists') end
  return saved
end
local removed = redis.pcall('DEL', KEYS[1])
if type(removed) == 'table' and removed.err then
  redis.call('DEL', KEYS[2], KEYS[3])
  return removed
end
return {'ok', ARGV[5]}
`)

// The successor is discovered from immutable cached metadata, then checked
// together with that metadata in Lua. Logout/another rotation cannot revive it.
var validateTokenResultScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if redis.call('GET', KEYS[2]) ~= ARGV[2] then return 0 end
return 1
`)

func (s *RedisStore) RotateToken(ctx context.Context, input TokenRotation) (*TokenRotationResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if input.OldKey == "" || input.NewKey == "" || input.ResultKey == "" ||
		input.OldKey == input.NewKey || input.OldKey == input.ResultKey || input.NewKey == input.ResultKey ||
		input.RequestID == "" || input.UserID == "" || input.EncryptedResult == "" ||
		input.TokenTTL.Milliseconds() <= 0 || input.ResultTTL.Milliseconds() <= 0 {
		return nil, errors.New("invalid token rotation")
	}
	record := TokenRotationResult{RequestID: input.RequestID, UserID: input.UserID, NewKey: input.NewKey, EncryptedResult: input.EncryptedResult}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	result, err := rotateTokenScript.Run(ctx, s.client, []string{input.OldKey, input.NewKey, input.ResultKey},
		input.UserID, input.RequestID, input.TokenTTL.Milliseconds(), input.ResultTTL.Milliseconds(), string(encoded)).StringSlice()
	if err != nil {
		return nil, err
	}
	if len(result) != 2 {
		return nil, errors.New("invalid rotation response")
	}
	if result[0] == "revoked" {
		return nil, nil
	}
	if result[0] != "ok" {
		return nil, errors.New("unknown rotation response")
	}
	if err := json.Unmarshal([]byte(result[1]), &record); err != nil {
		return nil, err
	}
	if record.UserID != input.UserID || record.RequestID != input.RequestID || record.NewKey == "" || record.EncryptedResult == "" {
		return nil, errors.New("invalid cached rotation result")
	}
	valid, err := validateTokenResultScript.Run(ctx, s.client, []string{input.ResultKey, record.NewKey}, result[1], input.UserID).Int()
	if err != nil {
		return nil, err
	}
	if valid != 1 {
		return nil, nil
	}
	return &record, nil
}

var revokeTokenScript = redis.NewScript(`
local cached = redis.call('GET', KEYS[2]) or ''
if cached ~= ARGV[2] then return 0 end
local keys = {KEYS[2]}
if redis.call('GET', KEYS[1]) == ARGV[1] then table.insert(keys, KEYS[1]) end
if ARGV[2] ~= '' and redis.call('GET', KEYS[3]) == ARGV[1] then
  table.insert(keys, KEYS[3])
end
redis.call('DEL', unpack(keys))
return 1
`)

// Retry metadata discovery if rotation raced it. Also revoke the successor
// when logout still presents the old token whose response was lost.
func (s *RedisStore) RevokeToken(ctx context.Context, oldKey, resultKey, userID string) error {
	if err := s.ready(); err != nil {
		return err
	}
	for {
		raw, found, err := s.GetString(ctx, resultKey)
		if err != nil {
			return err
		}
		nextKey := oldKey
		if found {
			var record TokenRotationResult
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				return err
			}
			if record.UserID != userID || record.NewKey == "" {
				return errors.New("invalid cached rotation owner")
			}
			nextKey = record.NewKey
		}
		n, err := revokeTokenScript.Run(ctx, s.client, []string{oldKey, resultKey, nextKey}, userID, raw).Int()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
