// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package testblob implements a blobstore appropriate for testing.
package testblob

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"io/ioutil"
	"sort"
	"strings"
	"sync"

	"github.com/grailbio/base/errors"
	"github.com/grailbio/reflow"
	"github.com/grailbio/reflow/blob"
)

type store struct {
	scheme  string
	mu      sync.Mutex
	buckets map[string]*bucket
}

// New returns a new blob store appropriate for testing. The returned
// store is empty and stored contents are kept in memory. New buckets
// are created as they are referenced. The implementation is not efficient
// for large numbers of keys. The provided scheme is used to construct
// object locations in the returned store.
func New(scheme string) blob.Store {
	return &store{
		scheme:  scheme,
		buckets: make(map[string]*bucket),
	}
}

func (s *store) Bucket(ctx context.Context, name string) (blob.Bucket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buckets[name]
	if !ok {
		b = &bucket{
			name:    fmt.Sprintf("%s://%s/", s.scheme, name),
			objects: make(map[string][]byte),
		}
		s.buckets[name] = b
	}
	return b, nil
}

type bucket struct {
	name    string
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *bucket) get(key string) ([]byte, bool) {
	b.mu.Lock()
	p, ok := b.objects[key]
	b.mu.Unlock()
	return p, ok
}

func (b *bucket) file(key string) (reflow.File, []byte, bool) {
	p, ok := b.get(key)
	return reflow.File{
		Size:   int64(len(p)),
		Source: fmt.Sprintf("%s/%s", b.name, key),
		ETag:   fmt.Sprint(crc32.Checksum(p, crc32.IEEETable)),
	}, p, ok
}

func (b *bucket) File(ctx context.Context, key string) (reflow.File, error) {
	file, _, ok := b.file(key)
	if !ok {
		return reflow.File{}, errors.E("testblob.File", b.name, key, errors.NotExist)
	}
	return file, nil
}

type scanner struct {
	scanned bool
	b       *bucket
	keys    []string
}

func (s *scanner) Scan(ctx context.Context) bool {
	if s.scanned {
		s.keys = s.keys[1:]
	} else {
		s.scanned = true
	}
	return len(s.keys) != 0
}

func (s *scanner) Err() error {
	return nil
}

func (s *scanner) Key() string {
	return s.keys[0]
}

func (s scanner) File() reflow.File {
	file, _, ok := s.b.file(s.Key())
	if !ok {
		panic("file notexist")
	}
	return file
}

func (b *bucket) Scan(prefix string) blob.Scanner {
	s := &scanner{b: b}
	for k := range b.objects {
		if strings.HasPrefix(k, prefix) {
			s.keys = append(s.keys, k)
		}
	}
	sort.Strings(s.keys)
	return s
}

func (b *bucket) Download(ctx context.Context, key, etag string, size int64, w io.WriterAt) (int64, error) {
	file, p, ok := b.file(key)
	if !ok {
		return -1, errors.E("testblob.Download", b.name, key, errors.NotExist)
	}
	if etag != "" && etag != file.ETag {
		return -1, errors.E("testblob.Download", b.name, key, errors.Precondition)
	}
	n, err := w.WriteAt(p, 0)
	return int64(n), err
}

func (b *bucket) Get(ctx context.Context, key string, etag string) (io.ReadCloser, reflow.File, error) {
	file, p, ok := b.file(key)
	if !ok {
		return nil, reflow.File{}, errors.E("testblob.Download", b.name, key, errors.NotExist)
	}
	if etag != "" && etag != file.ETag {
		return nil, reflow.File{}, errors.E("testblob.Download", b.name, key, errors.Precondition)
	}
	return ioutil.NopCloser(bytes.NewReader(p)), file, nil
}

func (b *bucket) Put(ctx context.Context, key string, size int64, body io.Reader) error {
	p, err := ioutil.ReadAll(body)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.objects[key] = p
	b.mu.Unlock()
	return nil
}

func (b *bucket) Snapshot(ctx context.Context, prefix string) (reflow.Fileset, error) {
	if !strings.HasSuffix(prefix, "/") {
		file, _, ok := b.file(prefix)
		if !ok {
			return reflow.Fileset{}, errors.E("testblob.Snapshot", b.name, prefix, errors.NotExist)
		}
		return reflow.Fileset{Map: map[string]reflow.File{".": file}}, nil
	}
	var keys []string
	b.mu.Lock()
	for key := range b.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	b.mu.Unlock()
	fs := reflow.Fileset{Map: make(map[string]reflow.File, len(keys))}
	sort.Strings(keys)
	for _, key := range keys {
		file, _, ok := b.file(key)
		if ok {
			fs.Map[key] = file
		}
	}
	return fs, nil
}

func (b *bucket) Copy(ctx context.Context, src, dst string) error {
	p, ok := b.get(src)
	if !ok {
		return errors.E("testblob.Copy", b.name, src, dst, errors.NotExist)
	}
	return b.Put(ctx, dst, 0, bytes.NewReader(p))
}

func (b *bucket) Delete(ctx context.Context, keys ...string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, key := range keys {
		delete(b.objects, key)
	}
	return nil
}

func (b *bucket) Location() string {
	return b.name
}