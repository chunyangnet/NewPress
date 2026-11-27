// Package testwasm builds bounded real guests used by integration tests.
package testwasm

import "encoding/binary"

// HostSequence invokes the listed static requests and returns the last response.
func HostSequence(requests ...[]byte) []byte {
	out := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	section := func(id byte, data []byte) {
		out = append(out, id)
		out = binary.AppendUvarint(out, uint64(len(data)))
		out = append(out, data...)
	}
	signed := func(value int64) []byte {
		var data []byte
		for {
			b := byte(value & 127)
			value >>= 7
			done := value == 0 && b&64 == 0 || value == -1 && b&64 != 0
			if !done {
				b |= 128
			}
			data = append(data, b)
			if done {
				return data
			}
		}
	}
	constant := func(value int64) []byte { return append([]byte{0x41}, signed(value)...) }
	section(1, []byte{5, 0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f, 0x60, 0, 1, 0x7e, 0x60, 1, 0x7f, 1, 0x7f, 0x60, 2, 0x7f, 0x7f, 0, 0x60, 2, 0x7f, 0x7f, 1, 0x7e})
	section(2, append([]byte{1, 8, 'n', 'e', 'w', 'p', 'r', 'e', 's', 's', 9, 'h', 'o', 's', 't', '_', 'c', 'a', 'l', 'l'}, 0, 0))
	section(3, []byte{4, 1, 2, 3, 4})
	section(5, []byte{1, 1, 4, 4})
	exports := []byte{5}
	for i, name := range []string{"np_api_version", "np_alloc", "np_free", "np_handle", "memory"} {
		exports = append(exports, byte(len(name)))
		exports = append(exports, name...)
		if i == 4 {
			exports = append(exports, 2, 0)
		} else {
			exports = append(exports, 0, byte(i+1))
		}
	}
	section(7, exports)
	const responseAt = 131072
	handle := []byte{1, 1, 0x7f} // one local: the final host response length
	data := binary.AppendUvarint(nil, uint64(len(requests)+1))
	segment := func(offset int, value []byte) {
		data = append(data, 0)
		data = append(data, constant(int64(offset))...)
		data = append(data, 0x0b)
		data = binary.AppendUvarint(data, uint64(len(value)))
		data = append(data, value...)
	}
	offset := 1024
	for _, request := range requests {
		segment(offset, request)
		for _, value := range []int{offset, len(request), responseAt + 8, 65536} {
			handle = append(handle, constant(int64(value))...)
		}
		handle = append(handle, 0x10, 0, 0x21, 2) // call host_call; local.set length
		offset += len(request)
	}
	segment(responseAt, []byte(`{"data": `))
	handle = append(handle, constant(responseAt+8)...)
	handle = append(handle, 0x20, 2, 0x6a) // end of final host response
	handle = append(handle, constant('}')...)
	handle = append(handle, 0x3a, 0, 0) // i32.store8
	handle = append(handle, 0x42)
	handle = append(handle, signed(int64(responseAt)<<32)...)
	handle = append(handle, 0x20, 2)
	handle = append(handle, constant(9)...)
	handle = append(handle, 0x6a, 0xad, 0x84, 0x0b) // length+9; extend; or packed address
	bodies := [][]byte{{0, 0x42, 1, 0x0b}, append(append([]byte{0}, constant(64)...), 0x0b), {0, 0x0b}, handle}
	code := []byte{4}
	for _, body := range bodies {
		code = binary.AppendUvarint(code, uint64(len(body)))
		code = append(code, body...)
	}
	section(10, code)
	section(11, data)
	return out
}
