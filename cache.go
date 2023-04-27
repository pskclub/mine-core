package core

import (
	"context"
	"fmt"
	"github.com/go-redis/redis/v8"
	"github.com/pskclub/mine-core/utils"
	"strings"
	"time"
)

type ICache interface {
	Set(key string, value interface{}, expiration time.Duration) error
	SetJSON(key string, value interface{}, expiration time.Duration) error
	Get(dest interface{}, key string) error
	GetJSON(dest interface{}, key string) error
	Del(key string) error
	Close()
}

type DatabaseCache struct {
	Host string
	Port string
}

type cache struct {
	rdb *redis.Client
}

var ctx = context.Background()

func NewCache(env *ENVConfig) *DatabaseCache {
	return &DatabaseCache{
		Host: env.CacheHost,
		Port: env.CachePort,
	}
}

func (r DatabaseCache) Connect() (ICache, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", r.Host, r.Port),
	})

	status := rdb.Ping(ctx)
	if status.Err() != nil {
		return nil, status.Err()
	}

	return &cache{rdb}, nil
}

func (c cache) Close() {
	err := c.rdb.Close()
	if err != nil {
		panic(err)
	}
}

func (c cache) Set(key string, value interface{}, expiration time.Duration) error {
	return c.rdb.Set(ctx, key, value, expiration).Err()
}

func (c cache) Get(dest interface{}, key string) error {
	return c.rdb.Get(ctx, key).Scan(dest)
}

func (c cache) Del(key string) error {
	if strings.Contains(key, "*") {
		iter := c.rdb.Scan(ctx, 0, key, 0).Iterator()
		if err := iter.Err(); err != nil {
			return err
		}

		for iter.Next(ctx) {
			err := c.rdb.Del(ctx, iter.Val()).Err()
			if err != nil {
				return err
			}
		}

		return nil
	} else {
		return c.rdb.Del(ctx, key).Err()
	}
}

func (c cache) SetJSON(key string, value interface{}, expiration time.Duration) error {
	newVal := utils.JSONToString(value)
	return c.Set(key, newVal, expiration)
}

func (c cache) GetJSON(dest interface{}, key string) error {
	var str string
	err := c.Get(&str, key)
	if err != nil {
		return err
	}

	return utils.JSONParse(utils.StringToBytes(str), dest)
}
