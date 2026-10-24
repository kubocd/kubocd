/*
Copyright 2026 Kubotal

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
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

type entry struct {
	name     string
	inserted time.Time
}

func (e *entry) GetInsertionTime() time.Time   { return e.inserted }
func (e *entry) SetInsertionTime(at time.Time) { e.inserted = at }
func (e *entry) String() string                { return e.name }

// keys returns the keys that the cache holds, and checks its weight.
func keys(t *testing.T, c Cache, wantWeight int64) []string {
	t.Helper()
	held := make([]string, 0, len(c.(*impl).mapping))
	for key := range c.(*impl).mapping {
		held = append(held, key)
	}
	slices.Sort(held)
	if weight := c.(*impl).weight; weight != wantWeight {
		t.Errorf("got the weight %d, want %d", weight, wantWeight)
	}
	return held
}

func TestTheCacheHoldsAtMostItsNumberOfEntries(t *testing.T) {
	c := NewCache(time.Hour, 2, 100, logr.Discard())
	for _, key := range []string{"a", "b", "c"} {
		c.Set(key, &entry{name: key}, 1)
	}
	if got := keys(t, c, 2); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("got %v, want the oldest entry gone", got)
	}
	if c.Get("a") != nil || c.Get("c") == nil {
		t.Fatalf("got a or no c")
	}
}

func TestTheCacheHoldsAtMostItsWeight(t *testing.T) {
	c := NewCache(time.Hour, 100, 10, logr.Discard())
	c.Set("a", &entry{name: "a"}, 4)
	c.Set("b", &entry{name: "b"}, 4)
	c.Set("c", &entry{name: "c"}, 4)
	if got := keys(t, c, 8); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("got %v, want the oldest entry gone", got)
	}
	c.Set("d", &entry{name: "d"}, 10)
	if got := keys(t, c, 10); !slices.Equal(got, []string{"d"}) {
		t.Fatalf("got %v, want the others gone for an entry of the whole weight", got)
	}
}

func TestAnEntryHeavierThanTheCacheIsNotStored(t *testing.T) {
	c := NewCache(time.Hour, 100, 10, logr.Discard())
	c.Set("a", &entry{name: "a"}, 3)
	c.Set("b", &entry{name: "b"}, 3)
	c.Set("b", &entry{name: "b2"}, 11)
	if got := keys(t, c, 3); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("got %v, want the heavy entry out, and the older one of its key gone", got)
	}
}

func TestAReplacedEntryCountsOnce(t *testing.T) {
	c := NewCache(time.Hour, 2, 10, logr.Discard())
	c.Set("a", &entry{name: "a"}, 4)
	c.Set("a", &entry{name: "a2"}, 6)
	c.Set("b", &entry{name: "b"}, 4)
	if got := keys(t, c, 10); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v, want both entries", got)
	}
	if got := c.Get("a").String(); got != "a2" {
		t.Fatalf("got %s, want the new value", got)
	}
}

func TestAnExpiredEntryNoLongerWeighs(t *testing.T) {
	c := NewCache(10*time.Millisecond, 100, 10, logr.Discard())
	c.Set("old", &entry{name: "old"}, 6)
	time.Sleep(20 * time.Millisecond)
	// The cleanup of a Get runs at most every 5 s
	c.(*impl).nextCleanup = time.Time{}
	if c.Get("old") != nil {
		t.Fatalf("got the expired entry")
	}
	c.Set("a", &entry{name: "a"}, 6)
	c.Set("b", &entry{name: "b"}, 4)
	if got := keys(t, c, 10); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v, want both new entries, the expired one weighing nothing", got)
	}
}

func TestExpiredEntriesGoBeforeTheOthers(t *testing.T) {
	c := NewCache(50*time.Millisecond, 3, 100, logr.Discard())
	c.Set("old1", &entry{name: "old1"}, 1)
	c.Set("old2", &entry{name: "old2"}, 1)
	time.Sleep(60 * time.Millisecond)
	c.Set("a", &entry{name: "a"}, 1)
	c.Set("b", &entry{name: "b"}, 1)
	if got := keys(t, c, 2); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v, want every expired entry gone once the cache is full", got)
	}
}
