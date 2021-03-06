// Copyright 2018 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

// exercise functionality to store/retrieve kernel cache.

import (
	"os"
	"io/ioutil"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/internal/testutil"
)

// DataNode is a nodefs.Node that Reads static data.
//
// Since Read works without Open, the kernel does not invalidate DataNode cache
// on user's open.
type DataNode struct {
	nodefs.Node

	data []byte
}

func NewDataNode(data []byte) *DataNode {
	return &DataNode{Node: nodefs.NewDefaultNode(), data: data}
}

func (d *DataNode) GetAttr(out *fuse.Attr, _ nodefs.File, _ *fuse.Context) fuse.Status {
	out.Size = uint64(len(d.data))
	out.Mode = fuse.S_IFREG | 0644
	return fuse.OK
}

func (d *DataNode) Read(_ nodefs.File, dest []byte, off int64, _ *fuse.Context) (fuse.ReadResult, fuse.Status) {
	l := int64(len(d.data))
	end := off + l
	if end > l {
		end = l
	}

	return fuse.ReadResultData(d.data[off:end]), fuse.OK
}

// TestCacheControl verifies that FUSE server process can store/retrieve kernel data cache.
func TestCacheControl(t *testing.T) {
	dir := testutil.TempDir()
	defer func() {
		err := os.Remove(dir)
		if err != nil {
			t.Fatal(err)
		}
	}()

	// setup a filesystem with 1 file
	root := nodefs.NewDefaultNode()
	opts := nodefs.NewOptions()
	opts.Debug = testutil.VerboseTest()
	srv, fsconn, err := nodefs.MountRoot(dir, root, opts)
	if err != nil {
		t.Fatal(err)
	}

	data0 := "hello world"
	file := NewDataNode([]byte(data0))
	root.Inode().NewChild("hello.txt", false, file)

	go srv.Serve()
	if err := srv.WaitMount(); err != nil {
		t.Fatal("WaitMount", err)
	}
	defer func() {
		err := srv.Unmount()
		if err != nil {
			t.Fatal(err)
		}
	}()

	// assertFileRead asserts that the file content reads as dataOK.
	assertFileRead := func(subj, dataOK string) {
		t.Helper()

		v, err := ioutil.ReadFile(dir + "/hello.txt")
		if err != nil {
			t.Fatalf("%s: file read: %s", subj, err)
		}
		if string(v) != dataOK {
			t.Fatalf("%s: file read: got %q  ; want %q", subj, v, dataOK)
		}
	}

	// make sure the file reads correctly
	assertFileRead("original", data0)

	// pin file content into OS cache
	f, err := os.Open(dir + "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	fmmap, err := unix.Mmap(int(f.Fd()), 0, len(data0), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		err := unix.Munmap(fmmap)
		if err != nil {
			t.Fatal(err)
		}
	}()
	err = unix.Mlock(fmmap)
	if err != nil {
		t.Fatal(err)
	}

	// assertMmapRead asserts that file's mmaped memory reads as dataOK.
	assertMmapRead := func(subj, dataOK string) {
		t.Helper()
		if string(fmmap) != dataOK {
			t.Fatalf("%s: file mmap: got %q  ; want %q", subj, fmmap, dataOK)
		}
	}

	// make sure the cache has original data
	assertMmapRead("original", data0)

	// store changed data into OS cache
	st := fsconn.FileNotifyStoreCache(file.Inode(), 7, []byte("123"))
	if st != fuse.OK {
		t.Fatalf("store cache: %s", st)
	}

	// make sure mmaped data and file read as updated data
	data1 := "hello w123d"
	assertMmapRead("after storecache", data1)
	assertFileRead("after storecache", data1)

	// TODO verify retrieve cache

	// invalidate cache
	st = fsconn.FileNotify(file.Inode(), 0, 0)
	if st != fuse.OK {
		t.Fatalf("invalidate cache: %s", st)
	}

	// make sure mmapped data and file read as original data
	assertMmapRead("after invalcache", data0)
	assertFileRead("after invalcache", data0)
}
