package cache

import (
	"bufio"
	"bytes"
	"fmt"
	"testing"
)

func FuzzRESP(f *testing.F) {
	for _, wire := range [][]byte{[]byte("+OK\r\n"), []byte("-ERR denied\r\n"), []byte(":-42\r\n"), []byte("$3\r\na\x00b\r\n"), []byte("$-1\r\n"), []byte("$0\r\n\r\n"), []byte("$99999999999999999999\r\n"), []byte("$4097\r\n"), []byte("*2\r\n"), []byte("$2\na\r\n")} {
		f.Add(wire)
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 8192 {
			t.Skip()
		}
		response, err := readRESPWithLimit(bufio.NewReader(bytes.NewReader(wire)), 4096)
		if err != nil {
			return
		}
		if len(response.bulk) > 4096 {
			t.Fatal("bulk response bypassed allocation limit")
		}
		if response.bulk != nil {
			encoded := fmt.Appendf(nil, "$%d\r\n", len(response.bulk))
			encoded = append(encoded, response.bulk...)
			encoded = append(encoded, '\r', '\n')
			next, err := readRESPWithLimit(bufio.NewReader(bytes.NewReader(encoded)), 4096)
			if err != nil || !bytes.Equal(next.bulk, response.bulk) || next.nil {
				t.Fatal("bulk response did not round trip")
			}
		}
	})
}
