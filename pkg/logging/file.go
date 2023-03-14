package logging

import (
	"io/fs"
	"os"
)

// ReopenFile is a wrapper of os.File that can be reopened.
//
// Works around loggo's inability to retrieve the io.Writer.
type ReopenFile struct {
	*os.File
}

func NewReopenFile() *ReopenFile {
	return &ReopenFile{}
}

func (f *ReopenFile) OpenFile(name string, flag int, perm fs.FileMode) (err error) {
	if f.File != nil {
		// Close may return an error, but we don't care.
		_ = f.Close()
	}
	f.File, err = os.OpenFile(name, flag, perm)
	return
}

func (f *ReopenFile) Close() error {
	err := f.File.Close()
	f.File = nil
	return err
}
