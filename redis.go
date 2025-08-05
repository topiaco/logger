package logger

import (
	"github.com/redis/go-redis/v9"
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
