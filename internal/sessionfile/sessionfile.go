// Package sessionfile recognises and reads agent session transcripts in
// every on-disk form: plain JSONL, and the zstd-compressed JSONL that
// newer codex versions rewrite older rollouts into (.jsonl.zst). Callers
// get one id rule and a reader that is always plain JSONL.
package sessionfile

import (
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	plainExt      = ".jsonl"
	compressedExt = ".jsonl.zst"
)

// IsTranscript reports whether a file name is a session transcript.
func IsTranscript(name string) bool {
	return strings.HasSuffix(name, plainExt) || strings.HasSuffix(name, compressedExt)
}

// Compressed reports whether a transcript is zstd-compressed. Compressed
// transcripts are rewritten whole, never appended to.
func Compressed(name string) bool {
	return strings.HasSuffix(name, compressedExt)
}

// Stem strips the transcript extension, leaving the session id (claude)
// or the rollout name (codex).
func Stem(name string) string {
	if strings.HasSuffix(name, compressedExt) {
		return strings.TrimSuffix(name, compressedExt)
	}
	return strings.TrimSuffix(name, plainExt)
}

// Open returns the transcript's plain JSONL, decompressing if needed.
func Open(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !Compressed(path) {
		return file, nil
	}
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		file.Close()
		return nil, err
	}
	return &zstdFile{decoder: decoder, file: file}, nil
}

type zstdFile struct {
	decoder *zstd.Decoder
	file    *os.File
}

func (z *zstdFile) Read(p []byte) (int, error) { return z.decoder.Read(p) }

func (z *zstdFile) Close() error {
	z.decoder.Close()
	return z.file.Close()
}
