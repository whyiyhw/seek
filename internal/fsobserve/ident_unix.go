//go:build unix

package fsobserve

import (
	"os"
	"syscall"
)

// fileIdent returns the (device, inode) pair identifying the file behind
// fi. Together they are stable for the LIFETIME of a file and change the
// moment a path is backed by a different file — which is what makes them
// worth carrying alongside size and mtime.
//
// Dev is int32 on darwin and uint64 on linux; the conversion compiles on
// both and is lossless for any real device number.
func fileIdent(fi os.FileInfo) (dev, ino uint64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), uint64(st.Ino)
}
