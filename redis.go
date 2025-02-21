package logger

import (
	"github.com/go-redis/redis"
)

var RedisDB *redis.Client

func InitRedis(options *redis.Options) {
	if options != nil {
		RedisDB = redis.NewClient(options)
	}
}

func NewRedisClient() *redis.Client {
	return RedisDB
}
