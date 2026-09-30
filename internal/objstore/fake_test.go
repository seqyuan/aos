package objstore

import (
	"context"
	"hash/crc64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// fakeBackend 内存版 Backend，用于离线跑通 Upload/Download/LS/RM 编排。
type fakeBackend struct {
	mu           sync.Mutex
	objs         map[string][]byte // key -> content
	uploads      []MultipartUpload
	putFileCalls int
	putBytesCall int
	getFileCalls int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{objs: map[string][]byte{}}
}

func fakeKey(bucket, key string) string { return bucket + "/" + key }

func (f *fakeBackend) ListPage(_ context.Context, bucket, prefix string, maxKeys int, token string) ([]Object, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objs {
		if strings.HasPrefix(k, bucket+"/") {
			objKey := strings.TrimPrefix(k, bucket+"/")
			if strings.HasPrefix(objKey, prefix) {
				keys = append(keys, objKey)
			}
		}
	}
	sort.Strings(keys)
	// token 为上一页最后一个 key；从其后开始
	start := 0
	for start < len(keys) && token != "" && keys[start] <= token {
		start++
	}
	if start >= len(keys) {
		return nil, "", nil
	}
	end := start + maxKeys
	next := ""
	if end < len(keys) {
		next = keys[end-1]
	} else {
		end = len(keys)
	}
	var out []Object
	for _, k := range keys[start:end] {
		content := f.objs[fakeKey(bucket, k)]
		out = append(out, Object{Key: k, Size: int64(len(content)), ETag: fakeETag(content)})
	}
	return out, next, nil
}

func (f *fakeBackend) Fingerprints(_ context.Context, bucket, prefix string, keys []string) (map[string]Fingerprint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	out := map[string]Fingerprint{}
	for k, content := range f.objs {
		if !strings.HasPrefix(k, bucket+"/") {
			continue
		}
		objKey := strings.TrimPrefix(k, bucket+"/")
		if !want[objKey] || !strings.HasPrefix(objKey, prefix) {
			continue
		}
		out[objKey] = Fingerprint{Size: int64(len(content)), CRC64: crc64.Checksum(content, crc64Table)}
	}
	return out, nil
}

func (f *fakeBackend) PutFile(_ context.Context, bucket, key, localPath string, _ TransferOpt) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putFileCalls++
	f.objs[fakeKey(bucket, key)] = data
	return nil
}

func (f *fakeBackend) PutBytes(_ context.Context, bucket, key string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putBytesCall++
	f.objs[fakeKey(bucket, key)] = append([]byte(nil), data...)
	return nil
}

func (f *fakeBackend) GetFile(_ context.Context, bucket, key, localPath string, _ int64, _ TransferOpt) error {
	f.mu.Lock()
	data, ok := f.objs[fakeKey(bucket, key)]
	f.getFileCalls++
	f.mu.Unlock()
	if !ok {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(localPath, data, 0o644)
}

func (f *fakeBackend) DeleteObject(_ context.Context, bucket, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objs, fakeKey(bucket, key))
	return nil
}

func (f *fakeBackend) DeleteObjects(_ context.Context, bucket string, keys []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		delete(f.objs, fakeKey(bucket, k))
	}
	return nil, nil
}

func (f *fakeBackend) ListUploads(_ context.Context, _, _ string) ([]MultipartUpload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]MultipartUpload(nil), f.uploads...), nil
}

func (f *fakeBackend) AbortUpload(_ context.Context, _, _, _ string) error { return nil }

func fakeETag(content []byte) string {
	const hexdigits = "0123456789abcdef"
	h := crc64.Checksum(content, crc64Table)
	var b [16]byte
	for i := 15; i >= 0; i-- {
		b[i] = hexdigits[h&0xf]
		h >>= 4
	}
	return string(b[:])
}
