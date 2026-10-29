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

package archiveio

import (
	"errors"
	"fmt"
	"math"
)

type Limits struct {
	StreamSize   int64
	ContentSize  int64
	Entries      int64
	MetadataSize int64
	ListingSize  int64
	Disabled     bool
}

func Defaults() Limits {
	return Limits{StreamSize: 47087616, ContentSize: 40 << 20, Entries: 1000, MetadataSize: 1 << 20, ListingSize: 1 << 20}
}

func (l Limits) Validate() error {
	for _, bound := range []struct {
		name  string
		value int64
	}{
		{"maxArchiveStreamSize", l.StreamSize}, {"maxArchiveContentSize", l.ContentSize},
		{"maxArchiveMetadataSize", l.MetadataSize}, {"maxArchiveListingSize", l.ListingSize},
	} {
		if bound.value < 1 || bound.value > math.MaxInt64/2 {
			return fmt.Errorf("archive limit %s must be between 1 and 4611686018427387903 bytes", bound.name)
		}
	}
	if l.Entries < 1 || l.Entries > 1048576 {
		return fmt.Errorf("archive entry limit must be between 1 and 1048576")
	}
	return nil
}

func resolve(options []*Limits) (Limits, error) {
	l := Defaults()
	if len(options) > 0 && options[0] != nil {
		l = *options[0]
	}
	return l, l.Validate()
}

var ErrLimit = errors.New("archive resource limit exceeded")

type limitError struct{ message string }

func (e *limitError) Error() string { return e.message }
func (e *limitError) Unwrap() error { return ErrLimit }

type budget struct {
	limits                                      Limits
	stream, content, entries, metadata, listing int64
}

func (b *budget) reserve(used *int64, n, max int64, format string) error {
	if b.limits.Disabled {
		return nil
	}
	if n < 0 || n > max-*used {
		return &limitError{message: fmt.Sprintf(format, max)}
	}
	*used += n
	return nil
}
func (b *budget) reserveStream(n int64) error {
	return b.reserve(&b.stream, n, b.limits.StreamSize, "archive decompressed stream exceeds %d bytes")
}
func (b *budget) reserveContent(n int64) error {
	return b.reserve(&b.content, n, b.limits.ContentSize, "archive content exceeds %d bytes")
}
func (b *budget) reserveEntries(n int64) error {
	return b.reserve(&b.entries, n, b.limits.Entries, "archive entries exceed %d")
}
func (b *budget) reserveMetadata(n int64) error {
	return b.reserve(&b.metadata, n, b.limits.MetadataSize, "archive metadata exceeds %d bytes")
}
func (b *budget) reserveListing(n int64) error {
	return b.reserve(&b.listing, n, b.limits.ListingSize, "archive listing exceeds %d bytes")
}
