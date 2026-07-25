package fake

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
)

// MinimalPNG returns a deterministic, valid PNG of w x h pixels. Every pixel is
// (v, v, v) where v is the seed byte; this keeps the image byte-stable for the
// same (w, h, v) so deterministic image checks can compute size/mean/variance
// predictably. The PNG is decodable by any compliant decoder.
//
// PNG layout: 8-byte signature, IHDR, IDAT (zlib-compressed raw scanlines with
// filter byte 0 per row), IEND.
func MinimalPNG(w, h int, v byte) []byte {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A})

	// IHDR
	var ihdr bytes.Buffer
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(w))
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(h))
	ihdr.WriteByte(8) // bit depth
	ihdr.WriteByte(2) // colour type RGB
	ihdr.WriteByte(0) // compression
	ihdr.WriteByte(0) // filter
	ihdr.WriteByte(0) // interlace
	writeChunk(&buf, "IHDR", ihdr.Bytes())

	// IDAT: each scanline = filter byte (0) + w*3 bytes of pixel data.
	var raw bytes.Buffer
	row := make([]byte, 1+w*3)
	row[0] = 0 // filter: none
	for i := 1; i < len(row); i++ {
		row[i] = v
	}
	for y := 0; y < h; y++ {
		raw.Write(row)
	}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	writeChunk(&buf, "IDAT", compressed.Bytes())

	writeChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

func writeChunk(buf *bytes.Buffer, ctype string, data []byte) {
	var lh [4]byte
	binary.BigEndian.PutUint32(lh[:], uint32(len(data)))
	buf.Write(lh[:])
	buf.WriteString(ctype)
	buf.Write(data)
	c := crc32.NewIEEE()
	_, _ = c.Write([]byte(ctype))
	_, _ = c.Write(data)
	var ch [4]byte
	binary.BigEndian.PutUint32(ch[:], c.Sum32())
	buf.Write(ch[:])
}
