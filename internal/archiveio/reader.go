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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
)

type Reader struct {
	gzip        *gzip.Reader
	tar         *tar.Reader
	stream      *streamReader
	budget      budget
	directories map[string]bool
	current     *tar.Header
	done        bool
	failure     error
}

func New(input io.Reader, options ...*Limits) (*Reader, error) {
	limits, err := resolve(options)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(input)
	if err != nil {
		return nil, archiveError(err)
	}
	r := &Reader{gzip: gz, budget: budget{limits: limits}, directories: make(map[string]bool)}
	r.stream = &streamReader{input: gz, budget: &r.budget, requireEnd: true}
	r.tar = tar.NewReader(r.stream)
	return r, nil
}

func archiveError(err error) error {
	if err == nil || errors.Is(err, ErrLimit) {
		return err
	}
	return fmt.Errorf("read archive: %w", err)
}

func (r *Reader) Close() error { return r.gzip.Close() }

func (r *Reader) Next() (*tar.Header, error) {
	if r.failure != nil {
		return nil, r.failure
	}
	if r.done {
		return nil, io.EOF
	}
	h, err := r.tar.Next()
	if err == io.EOF {
		if err = r.finish(); err != nil {
			r.failure = err
			return nil, err
		}
		r.done = true
		return nil, io.EOF
	}
	if err != nil {
		r.failure = archiveError(err)
		return nil, r.failure
	}
	if err = r.account(h); err != nil {
		r.failure = err
		return nil, err
	}
	r.current = h
	return h, nil
}

func (r *Reader) account(h *tar.Header) error {
	if err := r.budget.reserveContent(h.Size); err != nil {
		return err
	}
	if err := r.budget.reserveEntries(1); err != nil {
		return err
	}
	if r.budget.limits.Disabled {
		return nil
	}
	name := path.Clean(h.Name)
	for dir := path.Dir(name); dir != "." && dir != "/" && !r.directories[dir]; dir = path.Dir(dir) {
		if err := r.budget.reserveEntries(1); err != nil {
			return err
		}
		r.directories[dir] = true
	}
	if h.Typeflag == tar.TypeDir {
		r.directories[name] = true
	}
	return nil
}

func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.tar.Read(p)
	if err != nil && err != io.EOF {
		return n, archiveError(err)
	}
	return n, err
}

func (r *Reader) CopyMetadata(out io.Writer) error {
	r.budget.metadata = 0
	if err := r.budget.reserveMetadata(r.current.Size); err != nil {
		return err
	}
	_, err := io.Copy(out, r)
	return err
}

func (r *Reader) ReserveListing(n int64) error { return r.budget.reserveListing(n) }

func (r *Reader) finish() error {
	r.stream.requireEnd = false
	var buf [32768]byte
	for {
		n, err := r.stream.Read(buf[:])
		for _, b := range buf[:n] {
			if b != 0 {
				return fmt.Errorf("archive has data after its end marker")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return archiveError(err)
		}
	}
}

type streamReader struct {
	input      io.Reader
	budget     *budget
	requireEnd bool
	pending    error
}

func (r *streamReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.pending != nil {
		err := r.pending
		if err == io.EOF && r.requireEnd {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if !r.budget.limits.Disabled {
		remaining := r.budget.limits.StreamSize - r.budget.stream
		if int64(len(p)) > remaining+1 {
			p = p[:remaining+1]
		}
	}
	n, err := r.input.Read(p)
	if limitErr := r.budget.reserveStream(int64(n)); limitErr != nil {
		return 0, limitErr
	}
	// tar accepts a physical EOF without its two zero blocks; only its own end marker may end Next.
	if n > 0 {
		r.pending = err
		return n, nil
	}
	if err == io.EOF && r.requireEnd {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func ReadMetadata(input io.Reader, size int64, options ...*Limits) ([]byte, error) {
	limits, err := resolve(options)
	if err != nil {
		return nil, err
	}
	b := &budget{limits: limits}
	if err := b.reserveMetadata(size); err != nil {
		return nil, err
	}
	b.metadata = 0
	var out bytes.Buffer
	var buf [32768]byte
	for {
		p := buf[:]
		if !limits.Disabled {
			remaining := limits.MetadataSize - b.metadata
			if int64(len(p)) > remaining+1 {
				p = p[:remaining+1]
			}
		}
		n, err := input.Read(p)
		if limitErr := b.reserveMetadata(int64(n)); limitErr != nil {
			return nil, limitErr
		}
		_, _ = out.Write(p[:n])
		if err == io.EOF {
			return out.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}
