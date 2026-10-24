/*
Copyright 2025 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cache

import (
	"kubocd/internal/misc"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

type Entry interface {
	GetInsertionTime() time.Time
	SetInsertionTime(time.Time)
	String() string
}

type Cache interface {
	Get(key string) Entry
	// Set stores value under key, with its weight. The oldest entries go when
	// the cache would hold more entries, or more weight, than its caps.
	Set(key string, value Entry, weight int64)
}

type impl struct {
	mutex       sync.Mutex
	mapping     map[string]stored
	weight      int64
	nextCleanup time.Time
	logger      logr.Logger
	duration    time.Duration
	maxEntries  int
	maxWeight   int64
}

type stored struct {
	entry  Entry
	weight int64
}

var _ Cache = &impl{}

func (c *impl) Get(key string) Entry {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.clean()
	result := c.mapping[key].entry
	c.logger.V(1).Info("Cache get", "key", key, "resultTs", misc.TernaryF[string](result != nil, func() string { return result.String() }, func() string { return "UNSET" }))
	return result
}

func (c *impl) Set(key string, value Entry, weight int64) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.remove(key)
	if weight > c.maxWeight {
		c.logger.V(1).Info("Cache set refused, as the entry weighs more than the cache", "key", key, "weight", weight)
		return
	}
	value.SetInsertionTime(time.Now())
	c.mapping[key] = stored{entry: value, weight: weight}
	c.weight += weight
	c.logger.V(1).Info("Cache set", "key", key, "value", value.String(), "weight", weight)
	if len(c.mapping) > c.maxEntries || c.weight > c.maxWeight {
		c.removeOlderThan(time.Now().Add(-c.duration))
	}
	for len(c.mapping) > c.maxEntries || c.weight > c.maxWeight {
		c.remove(c.oldest())
	}
}

func (c *impl) remove(key string) {
	if old, ok := c.mapping[key]; ok {
		c.weight -= old.weight
		delete(c.mapping, key)
	}
}

func (c *impl) removeOlderThan(exp time.Time) {
	for k, v := range c.mapping {
		if v.entry.GetInsertionTime().Before(exp) {
			c.logger.V(1).Info("Cache cleaning up", "key", k, "value", v.entry.String())
			c.remove(k)
		}
	}
}

func (c *impl) oldest() string {
	oldest := ""
	for k, v := range c.mapping {
		if oldest == "" || v.entry.GetInsertionTime().Before(c.mapping[oldest].entry.GetInsertionTime()) {
			oldest = k
		}
	}
	return oldest
}

// To avoid having another process to manage, we call this on each Get().
// But, to avoid scanning all cache every time, we do this only after 5 sec
// We remove all stuff older than duration
// No lock, as we can be called only for a locker func
func (c *impl) clean() {
	now := time.Now()
	if now.After(c.nextCleanup) {
		c.logger.V(1).Info("Cache cleaning try")
		c.nextCleanup = now.Add(time.Second * 5)
		c.removeOlderThan(now.Add(-c.duration))
	}
}

// NewCache returns a cache whose entries expire after duration, and which
// holds at most maxEntries entries and maxWeight of weight.
func NewCache(duration time.Duration, maxEntries int, maxWeight int64, logger logr.Logger) Cache {
	return &impl{
		mapping:     make(map[string]stored),
		nextCleanup: time.Now().Add(time.Second * 5),
		duration:    duration,
		logger:      logger,
		maxEntries:  maxEntries,
		maxWeight:   maxWeight,
	}
}
