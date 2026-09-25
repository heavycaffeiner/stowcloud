//go:build linux

package vfs

import local "github.com/stowcloud/storage/local"

var ErrUnsupportedFilesystem = local.ErrUnsupportedFilesystem

type AdmissionError = local.AdmissionError
type Admission = local.Admission

func AdmitFsType(t FsType) (Admission, string) { return local.AdmitFsType(local.FsType(t)) }
func AdmitMount(path string, t FsType, hasBtime bool) (Admission, error) {
	return local.AdmitMount(path, local.FsType(t), hasBtime)
}
