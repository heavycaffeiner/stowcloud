//go:build linux

package mountinfo

import (
	"errors"
	"strings"
	"testing"
)

type failReader struct{}

func (failReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestParseTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []Mount
	}{
		{
			name: "empty reader",
			in:   "",
			want: nil,
		},
		{
			name: "no optional fields",
			in:   "36 35 98:0 / /mnt2 rw,noatime - ext3 /dev/root rw,errors=continue\n",
			want: []Mount{{Point: "/mnt2", FsType: "ext3"}},
		},
		{
			name: "several optional fields",
			in:   "36 35 98:0 / /mnt2 rw,noatime shared:1 master:2 propagate_from:3 - ext3 /dev/root rw\n",
			want: []Mount{{Point: "/mnt2", FsType: "ext3"}},
		},
		{
			name: "escaped space and backslash in mount point",
			in:   `36 35 98:0 / /srv/my\040folder\134thing rw - ext4 /dev/sdb1 rw` + "\n",
			want: []Mount{{Point: `/srv/my folder\thing`, FsType: "ext4"}},
		},
		{
			name: "malformed row between two good ones survives",
			in: "36 35 98:0 / /first rw - ext4 /dev/sda1 rw\n" +
				"garbage row with too few fields\n" +
				"37 35 98:1 / /second rw - xfs /dev/sda2 rw\n",
			want: []Mount{
				{Point: "/first", FsType: "ext4"},
				{Point: "/second", FsType: "xfs"},
			},
		},
		{
			name: "row truncated before separator",
			in:   "36 35 98:0 / /mnt2 rw,noatime shared:1\n",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.in))
			if err != nil {
				t.Fatalf("Parse: unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("Parse: got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("Parse[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseReaderError proves Parse surfaces a real reader failure rather
// than silently swallowing it the way a malformed line is swallowed.
func TestParseReaderError(t *testing.T) {
	_, err := Parse(failReader{})
	if err == nil {
		t.Fatal("Parse: want error from a failing reader, got nil")
	}
}

// TestParseContainerShape pastes verbatim rows measured from a real podman
// container: an overlay root, a proc mount, and an xfs bind mount. It
// pins the exact points and fstypes this package must return for the shape
// the sandbox discovery code actually runs against.
func TestParseContainerShape(t *testing.T) {
	const in = `355 354 0:65 / / rw,relatime master:97 - overlay overlay rw,lowerdir=/var/lib/containers/storage/overlay/l/ABC:/var/lib/containers/storage/overlay/l/DEF,upperdir=/var/lib/containers/storage/overlay/XYZ/diff,workdir=/var/lib/containers/storage/overlay/XYZ/work
356 355 0:66 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
357 355 0:67 / /srv/shares/photos rw,relatime master:120 - xfs /dev/mapper/data-photos rw,noatime,attr2,inode64,logbufs=8,logbsize=32k,noquota
`
	got, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	want := []Mount{
		{Point: "/", FsType: "overlay"},
		{Point: "/proc", FsType: "proc"},
		{Point: "/srv/shares/photos", FsType: "xfs"},
	}
	if len(got) != len(want) {
		t.Fatalf("Parse: got %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Parse[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
