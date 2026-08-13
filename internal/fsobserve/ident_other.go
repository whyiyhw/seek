//go:build !unix

package fsobserve

import "os"

// fileIdent has no portable answer off Unix: Windows exposes a file index
// only through GetFileInformationByHandle, which needs an open handle
// rather than the FileInfo we have here.
//
// Returning (0, 0) for every file degrades the token to size+mtime, which
// is the pre-existing behaviour — the identity check simply never fires.
// That is the right failure direction: a weaker token can miss a change
// (and the O_EXCL create path still holds), whereas inventing an
// identity would make unrelated files compare equal.
func fileIdent(os.FileInfo) (dev, ino uint64) { return 0, 0 }
