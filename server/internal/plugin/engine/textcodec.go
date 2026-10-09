package engine

import (
	"strings"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// installTextCodec provides the WHATWG TextDecoder / TextEncoder subset that
// protocol plugins need to turn parseStream byte chunks into text (UTF-8 only;
// decode(chunk, {stream: true}) keeps an incomplete trailing sequence for the
// next call).
func installTextCodec(vm *goja.Runtime) {
	_ = vm.Set("TextDecoder", func(call goja.ConstructorCall) *goja.Object {
		if l := call.Argument(0); !goja.IsUndefined(l) {
			switch strings.ToLower(strings.TrimSpace(l.String())) {
			case "utf-8", "utf8", "unicode-1-1-utf-8":
			default:
				panic(vm.NewTypeError("TextDecoder 只支持 utf-8"))
			}
		}
		var pending []byte
		obj := call.This
		_ = obj.Set("encoding", "utf-8")
		_ = obj.Set("decode", func(c goja.FunctionCall) goja.Value {
			data := append(pending, bytesOf(vm, c.Argument(0))...)
			pending = nil
			if opts, ok := c.Argument(1).(*goja.Object); ok {
				if s := opts.Get("stream"); s != nil && s.ToBoolean() {
					cut := incompleteTail(data)
					pending = append([]byte(nil), data[cut:]...)
					data = data[:cut]
				}
			}
			return vm.ToValue(strings.ToValidUTF8(string(data), "�"))
		})
		return nil
	})
	_ = vm.Set("TextEncoder", func(call goja.ConstructorCall) *goja.Object {
		obj := call.This
		_ = obj.Set("encoding", "utf-8")
		_ = obj.Set("encode", func(c goja.FunctionCall) goja.Value {
			s := ""
			if a := c.Argument(0); !goja.IsUndefined(a) {
				s = a.String()
			}
			return newUint8Array(vm, []byte(s))
		})
		return nil
	})
}

// bytesOf extracts the bytes of a Uint8Array / ArrayBuffer (or a string's
// UTF-8 bytes); anything else is a TypeError.
func bytesOf(vm *goja.Runtime, v goja.Value) []byte {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	switch x := v.Export().(type) {
	case []byte:
		return x
	case goja.ArrayBuffer:
		return x.Bytes()
	case string:
		return []byte(x)
	}
	panic(vm.NewTypeError("decode 需要 Uint8Array 或 ArrayBuffer"))
}

// incompleteTail returns the index where an incomplete UTF-8 sequence at the
// end of b starts (len(b) when b ends on a complete rune).
func incompleteTail(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return i
			}
			break
		}
	}
	return len(b)
}

// newUint8Array copies b into a new Uint8Array.
func newUint8Array(vm *goja.Runtime, b []byte) goja.Value {
	ab := vm.NewArrayBuffer(append([]byte(nil), b...))
	ctor := vm.Get("Uint8Array")
	arr, err := vm.New(ctor, vm.ToValue(ab))
	if err != nil {
		panic(err)
	}
	return arr
}
