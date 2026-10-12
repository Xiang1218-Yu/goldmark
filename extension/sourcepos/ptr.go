package sourcepos

import "unsafe"

// bytesPtr returns the address of the first element of b for use as an
// identity key. An index entry keeps a reference to the source slice, so
// the underlying array cannot be reused while an entry exists.
func bytesPtr(b []byte) uintptr {
	if len(b) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(unsafe.SliceData(b)))
}
