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
	"github.com/go-logr/logr"
	"kubocd/internal/misc"
	"sync"
	"time"
)

type Entry interface {
	GetInsertionTime() time.Time
	SetInsertionTime(time.Time)
	String() string
}

type Cache interface {
	Get(key string) Entry
	Set(key string, value Entry)
}

type impl struct {
	mutex       sync.Mutex
	mapping     map[string]Entry
	nextCleanup time.Time
	logger      logr.Logger
	duration    time.Duration
}

var _ Cache = &impl{}

func (c *impl) Get(key string) Entry {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.clean()
	result := c.mapping[key]
	c.logger.V(1).Info("Cache get", "key", key, "resultTs", misc.TernaryF[string](result != nil, func() string { return result.String() }, func() string { return "UNSET" }))
	return result
}

func (c *impl) Set(key string, value Entry) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	value.SetInsertionTime(time.Now())
	c.mapping[key] = value
	c.logger.V(1).Info("Cache set", "key", key, "value", value.String())

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
		exp := now.Add(-c.duration)
		for k, v := range c.mapping {
			if v.GetInsertionTime().Before(exp) {
				c.logger.V(1).Info("Cache cleaning up", "key", k, "value", v.String())
				delete(c.mapping, k)
			}
		}
	}
}

func NewCache(duration time.Duration, logger logr.Logger) Cache {
	return &impl{
		mapping:     make(map[string]Entry),
		nextCleanup: time.Now().Add(time.Second * 5),
		duration:    duration,
		logger:      logger,
	}
}
