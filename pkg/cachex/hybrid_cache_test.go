package cachex

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/samber/hot"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHybridCacheUpdateWithTTLInMemory(t *testing.T) {
	cache := NewHybridCache[int](HybridCacheConfig[int]{
		Namespace: Namespace("test-cache"),
		Memory:    func() *hot.HotCache[string, int] { return hot.NewHotCache[string, int](hot.LRU, 16).Build() },
	})

	err := cache.UpdateWithTTL("key", time.Minute, func(value int, found bool) (int, error) {
		assert.False(t, found)
		return value + 1, nil
	})
	require.NoError(t, err)
	value, found, err := cache.Get("key")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, value)
}

func TestHybridCacheUpdateWithTTLRedisIsAtomic(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	cache := NewHybridCache[int](HybridCacheConfig[int]{
		Namespace:    Namespace("test-cache"),
		Redis:        client,
		RedisCodec:   IntCodec{},
		RedisEnabled: func() bool { return true },
	})

	const updates = 32
	var wg sync.WaitGroup
	for i := 0; i < updates; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := cache.UpdateWithTTL("key", time.Minute, func(value int, found bool) (int, error) {
				return value + 1, nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	value, found, err := cache.Get("key")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, updates, value)
}

func TestHybridCacheUpdateWithTTLErrorsAreReturned(t *testing.T) {
	cache := NewHybridCache[int](HybridCacheConfig[int]{
		Namespace: Namespace("test-cache"),
		Memory:    func() *hot.HotCache[string, int] { return hot.NewHotCache[string, int](hot.LRU, 16).Build() },
	})
	want := errors.New("update failed")
	err := cache.UpdateWithTTL("key", time.Minute, func(value int, found bool) (int, error) {
		return 0, want
	})
	assert.ErrorIs(t, err, want)
}

func TestHybridCacheUpdateWithTTLRedisFailureIsReturned(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	cache := NewHybridCache[int](HybridCacheConfig[int]{
		Namespace:    Namespace("test-cache"),
		Redis:        client,
		RedisCodec:   IntCodec{},
		RedisEnabled: func() bool { return true },
	})
	server.Close()
	err := cache.UpdateWithTTL("key", time.Minute, func(value int, found bool) (int, error) {
		return value + 1, nil
	})
	assert.Error(t, err)
	_, _, getErr := cache.Get("key")
	assert.Error(t, getErr)
	_ = client.Close()
}
