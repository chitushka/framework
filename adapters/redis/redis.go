// Package redis provides supported go-redis adapters for cache, sessions, login
// throttling and leased queues. Server-side scripts make transitions atomic.
package redis

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/chitushka/framework/auth"
	"github.com/chitushka/framework/queue"
	sdk "github.com/redis/go-redis/v9"
)

// NewClient enforces finite I/O budgets and disables ambiguous automatic command
// replay. go-redis reconnects on the next operation after a broken connection.
func NewClient(options *sdk.Options) *sdk.Client {
	if options == nil {
		options = &sdk.Options{Addr: "localhost:6379"}
	}
	if options == nil {
		options = &sdk.Options{Addr: "localhost:6379"}
	}
	copy := *options
	copy.ContextTimeoutEnabled = true
	copy.MaxRetries = -1
	if copy.DialTimeout <= 0 {
		copy.DialTimeout = 5 * time.Second
	}
	if copy.ReadTimeout <= 0 {
		copy.ReadTimeout = 5 * time.Second
	}
	if copy.WriteTimeout <= 0 {
		copy.WriteTimeout = 5 * time.Second
	}
	if copy.TLSConfig != nil {
		copy.TLSConfig = copy.TLSConfig.Clone()
	}
	return sdk.NewClient(&copy)
}

type Store struct {
	client *sdk.Client
	prefix string
}

func New(client *sdk.Client, namespace string) (*Store, error) {
	if client == nil || namespace == "" || len(namespace) > 255 {
		return nil, errors.New("redis: client and namespace required")
	}
	hash := sha256.Sum256([]byte(namespace))
	return &Store{client: client, prefix: "chitu:{" + hex.EncodeToString(hash[:16]) + "}:"}, nil
}

func budget(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 5*time.Second)
}
func (s *Store) key(key string) string {
	h := sha256.Sum256([]byte(key))
	return s.prefix + "cache:" + hex.EncodeToString(h[:])
}
func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := budget(ctx)
	defer cancel()
	v, err := s.client.Get(ctx, s.key(key)).Bytes()
	if errors.Is(err, sdk.Nil) {
		return nil, false, nil
	}
	return v, err == nil, err
}
func (s *Store) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl < 0 || len(value) > 16<<20 {
		return errors.New("redis: invalid TTL/value exceeds 16 MiB")
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	return s.client.Set(ctx, s.key(key), value, ttl).Err()
}
func (s *Store) Delete(ctx context.Context, key string) error {
	ctx, cancel := budget(ctx)
	defer cancel()
	return s.client.Del(ctx, s.key(key)).Err()
}

var cas = sdk.NewScript(`
local old=redis.call('GET',KEYS[1])
if ARGV[1]=='absent' then if old then return 0 end
else if not old or old~=ARGV[2] then return 0 end end
if ARGV[3]=='set' and KEYS[1]~=KEYS[2] and redis.call('EXISTS',KEYS[2])==1 then return 0 end
redis.call('DEL',KEYS[1])
if ARGV[3]=='set' then
 if tonumber(ARGV[5])>0 then redis.call('PSETEX',KEYS[2],ARGV[5],ARGV[4]) else redis.call('SET',KEYS[2],ARGV[4]) end
end
return 1`)

func (s *Store) CompareAndSwap(ctx context.Context, old string, expected []byte, next string, value []byte, ttl time.Duration) (bool, error) {
	if ttl < 0 || len(value) > 16<<20 {
		return false, errors.New("redis: invalid TTL/value")
	}
	mode, operation := "present", "set"
	if expected == nil {
		mode = "absent"
	}
	if next == "" {
		operation = "delete"
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	n, err := cas.Run(ctx, s.client, []string{s.key(old), s.key(next)}, mode, expected, operation, value, milliseconds(ttl)).Int()
	return n == 1, err
}
func milliseconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d-1)/time.Millisecond) + 1
}

// Allow reserves BOTH counters or neither. All keys expire; no raw account/client
// identifiers are stored. Redis memory/noeviction capacity is an operator limit.
type Limiter struct {
	store  *Store
	config auth.AttemptLimitConfig
}

func NewLimiter(store *Store, config auth.AttemptLimitConfig) (*Limiter, error) {
	if store == nil {
		return nil, errors.New("redis: store required")
	}
	if config.AccountLimit == 0 {
		config.AccountLimit = 5
	}
	if config.ClientLimit == 0 {
		config.ClientLimit = 30
	}
	if config.Window == 0 {
		config.Window = time.Minute
	}
	if config.AccountLimit < 1 || config.ClientLimit < 1 || config.Window < time.Second || config.Window > 24*time.Hour {
		return nil, errors.New("redis: invalid limits")
	}
	return &Limiter{store, config}, nil
}

var limit = sdk.NewScript(`
local retry=0
for i=1,2 do if tonumber(redis.call('GET',KEYS[i]) or '0')>=tonumber(ARGV[i]) then local ttl=redis.call('PTTL',KEYS[i]);if ttl<0 then return redis.error_reply('limiter TTL missing') end;retry=math.max(retry,math.max(1,ttl)) end end
if retry>0 then return retry end
for i=1,2 do if redis.call('INCR',KEYS[i])==1 then redis.call('PEXPIRE',KEYS[i],ARGV[3]) end end
return 0`)

func (l *Limiter) Allow(ctx context.Context, account, client string) (time.Duration, error) {
	if account == "" || client == "" || len(account) > 256 || len(client) > 256 {
		return 0, errors.New("redis: invalid limiter keys")
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	n, err := limit.Run(ctx, l.store.client, []string{l.store.key("limit:account:" + account), l.store.key("limit:client:" + client)}, l.config.AccountLimit, l.config.ClientLimit, milliseconds(l.config.Window)).Int64()
	return time.Duration(n) * time.Millisecond, err
}

func (s *Store) queueKeys(name string) []string {
	h := sha256.Sum256([]byte(name))
	base := s.prefix + "queue:" + hex.EncodeToString(h[:16])
	return []string{base + ":ready", base + ":body", base + ":lease", base + ":owner", base + ":invalid"}
}
func nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var push = sdk.NewScript(`redis.call('HSET',KEYS[2],ARGV[1],ARGV[2]); redis.call('ZADD',KEYS[1],ARGV[3],ARGV[1]); return 1`)

func (s *Store) Push(ctx context.Context, name string, payload []byte, available time.Time) error {
	if len(payload) > 16<<20 {
		return errors.New("redis: message exceeds 16 MiB")
	}
	id, err := nonce()
	if err != nil {
		return err
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	return push.Run(ctx, s.client, s.queueKeys(name), id, payload, available.UnixMilli()).Err()
}

var reserve = sdk.NewScript(`
local clock=redis.call('TIME'); local now=clock[1]*1000+math.floor(clock[2]/1000)
local expired=redis.call('ZRANGEBYSCORE',KEYS[3],'-inf',now,'LIMIT',0,100)
for _,id in ipairs(expired) do redis.call('ZREM',KEYS[3],id);redis.call('HDEL',KEYS[4],id);redis.call('ZADD',KEYS[1],now,id) end
local due=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,1)
if #due==0 then return {} end
local id=due[1]; local body=redis.call('HGET',KEYS[2],id)
if not body then return redis.error_reply('queue body missing') end
redis.call('ZREM',KEYS[1],id);redis.call('ZADD',KEYS[3],now+tonumber(ARGV[2]),id);redis.call('HSET',KEYS[4],id,ARGV[1])
return {body,id..':'..ARGV[1]}`)

func (s *Store) Reserve(ctx context.Context, name string, _ time.Time, lease time.Duration) ([]byte, string, bool, error) {
	if lease <= 0 {
		return nil, "", false, errors.New("redis: lease must be positive")
	}
	receipt, err := nonce()
	if err != nil {
		return nil, "", false, err
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	values, err := reserve.Run(ctx, s.client, s.queueKeys(name), receipt, milliseconds(lease)).StringSlice()
	if err != nil {
		return nil, "", false, err
	}
	if len(values) == 0 {
		return nil, "", false, nil
	}
	if len(values) != 2 {
		return nil, "", false, errors.New("redis: invalid reservation")
	}
	return []byte(values[0]), values[1], true, nil
}

var settle = sdk.NewScript(`
if redis.call('HGET',KEYS[4],ARGV[1])~=ARGV[2] then return 0 end
local clock=redis.call('TIME');local now=clock[1]*1000+math.floor(clock[2]/1000)
local expiry=redis.call('ZSCORE',KEYS[3],ARGV[1]);if not expiry or tonumber(expiry)<=now then return 0 end
if ARGV[3]=='retry' then redis.call('HSET',KEYS[2],ARGV[1],ARGV[4]);redis.call('ZADD',KEYS[1],ARGV[5],ARGV[1]) else if ARGV[3]=='quarantine' then redis.call('HSET',KEYS[5],ARGV[1],ARGV[4]) end;redis.call('HDEL',KEYS[2],ARGV[1]) end
redis.call('ZREM',KEYS[3],ARGV[1]);redis.call('HDEL',KEYS[4],ARGV[1]);return 1`)

func (s *Store) settle(ctx context.Context, name, receipt, operation string, payload []byte, available time.Time) error {
	parts := strings.Split(receipt, ":")
	if len(parts) != 2 {
		return queue.ErrLeaseLost
	}
	ctx, cancel := budget(ctx)
	defer cancel()
	n, err := settle.Run(ctx, s.client, s.queueKeys(name), parts[0], parts[1], operation, payload, available.UnixMilli()).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return queue.ErrLeaseLost
	}
	return nil
}
func (s *Store) Ack(ctx context.Context, name, receipt string) error {
	return s.settle(ctx, name, receipt, "ack", nil, time.Time{})
}
func (s *Store) Retry(ctx context.Context, name, receipt string, payload []byte, available time.Time) error {
	return s.settle(ctx, name, receipt, "retry", payload, available)
}

// Purge removes only this named queue, for operator use after stopping consumers.
func (s *Store) Purge(ctx context.Context, name string) error {
	ctx, cancel := budget(ctx)
	defer cancel()
	return s.client.Del(ctx, s.queueKeys(name)...).Err()
}

var _ queue.RedisStore = (*Store)(nil)
var _ auth.AttemptLimiter = (*Limiter)(nil)
