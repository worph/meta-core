package watcher

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"io"
	"os"
	"strings"
)

const (
	// MidHashSampleSize is the size of the sample to hash (1MB)
	MidHashSampleSize = 1024 * 1024 // 1MB

	// MidHash256Code is the custom multicodec for midhash256
	MidHash256Code = 0x1000
)

// ComputeMidHash256 computes a midhash256 for a file.
//
// Algorithm:
// - For files <= 1MB: Hashes entire file content + size prefix
// - For files > 1MB: Hashes middle 1MB + size prefix
//
// The size prefix (8-byte big-endian) ensures that files with identical
// middle content but different sizes produce different hashes.
//
// fileSize must be passed in by the caller (typically from a FileInfo it
// already has from filepath.Walk or a previous os.Stat). This function
// deliberately does NOT call os.Stat itself — re-statting every file during
// a scan doubles the syscall count, which matters on slow / network drives.
//
// Returns: CID v1 string in base32lower format (prefixed with "b")
func ComputeMidHash256(filePath string, fileSize int64) (string, error) {
	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Create size buffer (8-byte big-endian)
	sizeBuffer := make([]byte, 8)
	binary.BigEndian.PutUint64(sizeBuffer, uint64(fileSize))

	// Read sample data
	var sampleData []byte
	if fileSize <= MidHashSampleSize {
		// Small file: read entire content
		sampleData = make([]byte, fileSize)
		_, err = io.ReadFull(file, sampleData)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", err
		}
	} else {
		// Large file: read middle 1MB
		middleOffset := (fileSize - MidHashSampleSize) / 2
		_, err = file.Seek(middleOffset, io.SeekStart)
		if err != nil {
			return "", err
		}
		sampleData = make([]byte, MidHashSampleSize)
		_, err = io.ReadFull(file, sampleData)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return "", err
		}
	}

	// Compute SHA-256 hash of [size + sample]
	hasher := sha256.New()
	hasher.Write(sizeBuffer)
	hasher.Write(sampleData)
	hashBytes := hasher.Sum(nil)

	// Build CID v1 bytes:
	// [version(0x01)] + [varint(codec 0x1000)] + [multihash]
	// multihash = [varint(hashCode 0x1000)] + [varint(length 32)] + [hash bytes]
	cidBytes := make([]byte, 0, 1+2+2+1+32)

	// CID version 1
	cidBytes = append(cidBytes, 0x01)

	// Content codec as varint (0x1000 = 4096)
	// varint encoding: 0x1000 = 0b0001_0000_0000_0000
	// Split into 7-bit chunks: 0b00_0000_0000 (high) and 0b010_0000 (low with continuation)
	// Result: [0x80, 0x20] for 0x1000
	cidBytes = append(cidBytes, 0x80, 0x20)

	// Multihash: hash function code as varint (0x1000)
	cidBytes = append(cidBytes, 0x80, 0x20)

	// Multihash: length as varint (32 = 0x20)
	cidBytes = append(cidBytes, 0x20)

	// Multihash: hash bytes
	cidBytes = append(cidBytes, hashBytes...)

	// Encode as base32lower (RFC 4648 without padding)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(cidBytes)
	encoded = strings.ToLower(encoded)

	// Add multibase prefix "b" for base32lower
	return "b" + encoded, nil
}
