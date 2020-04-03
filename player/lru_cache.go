package player

import (
	"os"
	"time"

	lru "github.com/hashicorp/golang-lru"
)

// LRUCacheOpts contains options for a cache. Size is max size in bytes
type LRUCacheOpts struct {
	Path          string
	Size          uint64
	SweepInterval time.Duration
}

type lruCache struct {
	storage *fsStorage
	lru     *lru.Cache
}

// InitNGCache initializes a cache for chunks.
func InitLRUCache(opts *LRUCacheOpts) (ChunkCache, error) {
	storage, err := initFSStorage(opts.Path)
	if err != nil {
		return nil, err
	}

	if opts.Size == 0 {
		opts.Size = defaultMaxCacheSize
	}

	onEvicted := func(key interface{}, value interface{}) {
		storage.remove(value)
	}

	lru, err := lru.NewWithEvict(int(opts.Size/ChunkSize), onEvicted)
	if err != nil {
		return nil, err
	}

	c := &lruCache{storage, lru}

	return c, nil
}

func (c *lruCache) Set(hash string, body []byte) (ReadableChunk, error) {
	var numWritten int
	Logger.Debugf("attempting to cache chunk %v", hash)
	chunkPath := c.storage.getPath(hash)

	f, err := os.OpenFile(chunkPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		MetCacheErrorCount.Inc()
		Logger.Debugf("chunk %v already exists on the local filesystem, not overwriting", hash)
	} else {
		numWritten, err = f.Write(body)
		defer f.Close()
		if err != nil {
			MetCacheErrorCount.Inc()
			Logger.Errorf("error saving cache file %v: %v", chunkPath, err)
			return nil, err
		}

		err = f.Close()
		if err != nil {
			MetCacheErrorCount.Inc()
			Logger.Errorf("error closing cache file %v: %v", chunkPath, err)
			return nil, err
		}
	}

	evicted := c.lru.Add(hash, hash)
	Logger.Debugf("cached %v bytes for chunk %v, evicted: %v", numWritten, hash, evicted)

	return &cachedChunk{reflectedChunk{body}}, nil
}

func (c *lruCache) Has(hash string) bool {
	return c.lru.Contains(hash)
}

func (c *lruCache) Get(hash string) (ReadableChunk, bool) {
	if value, ok := c.lru.Get(hash); ok {
		f, err := c.storage.open(value)
		if err != nil {
			MetCacheErrorCount.Inc()
			Logger.Errorf("chunk %v found in cache but couldn't open the file: %v", hash, err)
			c.lru.Remove(value)
			return nil, false
		}
		cb, err := initCachedChunk(f)
		if err != nil {
			Logger.Errorf("chunk %v found in cache but couldn't read the file: %v", hash, err)
			return nil, false
		}
		defer f.Close()
		return cb, true
	}

	Logger.Debugf("cache miss for chunk %v", hash)
	return nil, false
}

func (c *lruCache) Remove(hash string) {
	c.storage.remove(hash)
	c.lru.Remove(hash)
}

func (c *lruCache) Size() uint64 {
	return uint64(c.lru.Len()) * ChunkSize
}

func (c *lruCache) IsCacheRestored() chan bool {
	return make(chan bool)
}