// Copyright (c) 2019, Google Inc.
//
// Permission to use, copy, modify, and/or distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY
// SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION
// OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN
// CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

package runner

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const tagHandshake = byte('H')
const tagApplication = byte('A')
const tagAlert = byte('L')

type encryptionLevel byte

const (
	encryptionInitial     encryptionLevel = 0
	encryptionEarlyData   encryptionLevel = 1
	encryptionHandshake   encryptionLevel = 2
	encryptionApplication encryptionLevel = 3
)

// mockQUICTransport provides a record layer for sending/receiving messages
// when testing TLS over QUIC. It is only intended for testing, as it runs over
// an in-order reliable transport, looks nothing like the QUIC wire image, and
// provides no confidentiality guarantees. (In fact, it leaks keys in the
// clear.)
//
// Messages from TLS that are sent over a mockQUICTransport are wrapped in a
// TLV-like format. The first byte of a mockQUICTransport message is a tag
// indicating the TLS record type. This is followed by the 2 byte cipher suite
// ID of the cipher suite that would have been used to encrypt the record. Next
// is a 4-byte big-endian length indicating the length of the remaining payload.
// The payload starts with the key that would be used to encrypt the record, and
// the remainder of the payload is the plaintext of the TLS record. Note that
// the 4-byte length covers the length of the key and plaintext, but not the
// cipher suite ID or tag.
type mockQUICTransport struct {
	net.Conn
	readLevel, writeLevel             encryptionLevel
	readSecret, writeSecret           []byte
	readCipherSuite, writeCipherSuite uint16
	skipEarlyData                     bool
}

func newMockQUICTransport(conn net.Conn) *mockQUICTransport {
	return &mockQUICTransport{Conn: conn}
}

func (m *mockQUICTransport) read() (byte, []byte, error) {
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(m.Conn, header); err != nil {
			return 0, nil, err
		}
		tag := header[0]
		level := header[1]
		cipherSuite := binary.BigEndian.Uint16(header[2:4])
		length := binary.BigEndian.Uint32(header[4:])
		value := make([]byte, length)
		if _, err := io.ReadFull(m.Conn, value); err != nil {
			return 0, nil, fmt.Errorf("error reading record")
		}
		if level != byte(m.readLevel) {
			if m.skipEarlyData && level == byte(encryptionEarlyData) {
				continue
			}
			return 0, nil, fmt.Errorf("received level %d does not match expected %d", level, m.readLevel)
		}
		if cipherSuite != m.readCipherSuite {
			return 0, nil, fmt.Errorf("received cipher suite %d does not match expected %d", cipherSuite, m.readCipherSuite)
		}
		if len(m.readSecret) > len(value) {
			return 0, nil, fmt.Errorf("input length too short")
		}
		secret := value[:len(m.readSecret)]
		out := value[len(m.readSecret):]
		if !bytes.Equal(secret, m.readSecret) {
			return 0, nil, fmt.Errorf("secrets don't match: got %x but expected %x", secret, m.readSecret)
		}
		// Although not true for QUIC in general, our transport is ordered, so
		// we expect to stop skipping early data after a valid record.
		m.skipEarlyData = false
		return tag, out, nil
	}
}

func (m *mockQUICTransport) readRecord(want recordType) (recordType, *block, error) {
	typ, contents, err := m.read()
	if err != nil {
		return 0, nil, err
	}
	var returnType recordType
	if typ == tagHandshake {
		returnType = recordTypeHandshake
	} else if typ == tagApplication {
		returnType = recordTypeApplicationData
	} else if typ == tagAlert {
		returnType = recordTypeAlert
	} else {
		return 0, nil, fmt.Errorf("unknown type %d\n", typ)
	}
	return returnType, &block{contents, 0, nil}, nil
}

func (m *mockQUICTransport) writeRecord(typ recordType, data []byte) (int, error) {
	tag := tagHandshake
	if typ == recordTypeApplicationData {
		tag = tagApplication
	} else if typ != recordTypeHandshake {
		return 0, fmt.Errorf("unsupported record type %d\n", typ)
	}
	length := len(m.writeSecret) + len(data)
	payload := make([]byte, 1+1+2+4+length)
	payload[0] = tag
	payload[1] = byte(m.writeLevel)
	binary.BigEndian.PutUint16(payload[2:4], m.writeCipherSuite)
	binary.BigEndian.PutUint32(payload[4:8], uint32(length))
	copy(payload[8:], m.writeSecret)
	copy(payload[8+len(m.writeSecret):], data)
	if _, err := m.Conn.Write(payload); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (m *mockQUICTransport) Write(b []byte) (int, error) {
	panic("unexpected call to Write")
}

func (m *mockQUICTransport) Read(b []byte) (int, error) {
	panic("unexpected call to Read")
}
