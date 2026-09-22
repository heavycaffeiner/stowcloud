//go:build linux

package vault

import (
	"errors"
	"fmt"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/internal/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/stowcloud/veracrypt"
)

type publicFilesystem struct{ fs veracrypt.Filesystem }
func mapPublicError(err error) error {
	if err == nil { return nil }
	switch {
	case errors.Is(err, veracrypt.ErrWrongPassword): return fmt.Errorf("%w: %v", ErrWrongPassword, err)
	case errors.Is(err, veracrypt.ErrHeaderCorrupt): return fmt.Errorf("%w: %v", ErrHeaderCorrupt, err)
	case errors.Is(err, veracrypt.ErrUnsupportedVolume): return fmt.Errorf("%w: %v", ErrUnsupportedVolume, err)
	case errors.Is(err, veracrypt.ErrHeaderFieldsInvalid): return fmt.Errorf("%w: %v", ErrHeaderFieldsInvalid, err)
	case errors.Is(err, veracrypt.ErrUnsupportedFilesystem): return fmt.Errorf("%w: %v", ErrUnsupportedFilesystem, err)
	case errors.Is(err, veracrypt.ErrNotFound): return fmt.Errorf("%w: %v", vfs.ErrNotFound, err)
	case errors.Is(err, veracrypt.ErrDenied): return fmt.Errorf("%w: %v", vfs.ErrDenied, err)
	case errors.Is(err, veracrypt.ErrExists): return fmt.Errorf("%w: %v", vfs.ErrExists, err)
	case errors.Is(err, veracrypt.ErrNotEmpty): return fmt.Errorf("%w: %v", vfs.ErrNotEmpty, err)
	case errors.Is(err, veracrypt.ErrNoSpace): return fmt.Errorf("%w: %v", vfs.ErrNoSpace, err)
	case errors.Is(err, veracrypt.ErrNotADirectory): return fmt.Errorf("%w: %v", vfs.ErrNotADirectory, err)
	case errors.Is(err, veracrypt.ErrIsDirectory): return fmt.Errorf("%w: %v", vfs.ErrIsDirectory, err)
	default: return err
	}
}

func publicPath(p vfs.SafePath) (veracrypt.SafePath, error) { return veracrypt.ParseSafePath(p.String()) }
func (f *publicFilesystem) Alive() error { return mapPublicError(f.fs.Alive()) }
func (f *publicFilesystem) Space() (uint64,uint64) { return f.fs.Space() }
func (f *publicFilesystem) Stat(p vfs.SafePath) (StatInfo,error) { q,err:=publicPath(p);if err!=nil{return StatInfo{},err};s,err:=f.fs.Stat(q);if err!=nil{return StatInfo{},mapPublicError(err)};return StatInfo{IsDir:s.IsDir,Size:s.Size,MtimeNs:s.MtimeNs,Ino:s.Ino},nil }
func (f *publicFilesystem) ReadDir(p vfs.SafePath) ([]Dirent,error) { q,err:=publicPath(p);if err!=nil{return nil,err};es,err:=f.fs.ReadDir(q);if err!=nil{return nil,mapPublicError(err)};out:=make([]Dirent,len(es));for i,e:=range es{out[i]=Dirent{Name:e.Name,IsDir:e.IsDir,Ino:e.Ino}};return out,nil }
func (f *publicFilesystem) ReadFile(p vfs.SafePath,w io.Writer) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.ReadFile(q,w))}
func (f *publicFilesystem) WriteFileStaged(p vfs.SafePath,r io.Reader,n bool,m int64)(bool,error){q,err:=publicPath(p);if err!=nil{return false,err};ok,e:=f.fs.WriteFileStaged(q,r,n,m);return ok,mapPublicError(e)}
func (f *publicFilesystem) CreateFile(p vfs.SafePath) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.CreateFile(q))}
func (f *publicFilesystem) Truncate(p vfs.SafePath,n uint64) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.Truncate(q,n))}
func (f *publicFilesystem) SetModTime(p vfs.SafePath,n int64) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.SetModTime(q,n))}
func (f *publicFilesystem) Mkdir(p vfs.SafePath) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.Mkdir(q))}
func (f *publicFilesystem) Rmdir(p vfs.SafePath) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.Rmdir(q))}
func (f *publicFilesystem) Remove(p vfs.SafePath) error {q,err:=publicPath(p);if err!=nil{return err};return mapPublicError(f.fs.Remove(q))}
func (f *publicFilesystem) Rename(a,b vfs.SafePath,n bool) error {qa,err:=publicPath(a);if err!=nil{return err};qb,err:=publicPath(b);if err!=nil{return err};return mapPublicError(f.fs.Rename(qa,qb,n))}
func (f *publicFilesystem) Sync() error{return mapPublicError(f.fs.Sync())}

func openPublicFilesystem(c *veracrypt.Container,size uint64,clk clock.Clock)(filesystem,error){fs,err:=veracrypt.MountFilesystem(c,size,clk);if err!=nil{return nil,mapPublicError(err)};return &publicFilesystem{fs:fs},nil}
func createPublicContainer(path string,size uint64,p secret.Secret) error{return veracrypt.Create(path,size,p.Reveal())}
func openPublicContainer(path string,p secret.Secret,pim uint32,hash string)(*veracrypt.Container,uint64,error){c,s,e:=veracrypt.Open(path,p.Reveal(),pim,hash);return c,s,mapPublicError(e)}
