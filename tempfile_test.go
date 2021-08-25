// Copyright 2021 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build !windows

package renameio

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func withUmask(t *testing.T, mask os.FileMode) {
	t.Helper()

	old := syscall.Umask(int(mask))

	t.Cleanup(func() {
		syscall.Umask(old)
	})
}

func withCustomRng(t *testing.T, fn func() int64) {
	t.Helper()

	orig := nextrandom

	t.Cleanup(func() {
		nextrandom = orig
	})

	nextrandom = fn
}

func TestOpenTempFile(t *testing.T) {
	const count = 100

	// Install a deterministic random generator
	var next int64 = 12345
	withCustomRng(t, func() int64 {
		v := next
		next++
		return v
	})

	for _, umask := range []os.FileMode{0o000, 0o011, 0o007, 0o027, 0o077} {
		t.Run(fmt.Sprintf("0%o", umask), func(t *testing.T) {
			withUmask(t, umask)

			dir := t.TempDir()

			for i := 0; i < count; i++ {
				perm := [...]os.FileMode{0600, 0755, 0411}[i%3]
				maskedPerm := perm & ^umask

				got, err := openTempFile(dir, "test", perm)
				if err != nil {
					t.Errorf("openTempFile() failed: %v", err)
				}

				t.Cleanup(func() {
					if err := got.Close(); err != nil {
						t.Errorf("Close() failed: %v", err)
					}
				})

				if fi, err := os.Stat(got.Name()); err != nil {
					t.Errorf("Stat(%q) failed: %v", got.Name(), err)
				} else if gotPerm := fi.Mode() & os.ModePerm; gotPerm != maskedPerm {
					t.Errorf("Got permissions 0%o, want 0%o", gotPerm, maskedPerm)
				}
			}

			if entries, err := ioutil.ReadDir(dir); err != nil {
				t.Errorf("ReadDir(%q) failed: %v", dir, err)
			} else if len(entries) < count {
				t.Errorf("Directory %q contains fewer than %d entries", dir, count)
			}
		})
	}
}

func TestOpenTempFileConflict(t *testing.T) {
	withUmask(t, 0077)

	// https://xkcd.com/221/
	withCustomRng(t, func() int64 {
		return 4
	})

	dir := t.TempDir()

	if first, err := openTempFile(dir, "test", 0644); err != nil {
		t.Errorf("openTempFile() failed: %v", err)
	} else {
		first.Close()
	}

	if _, err := openTempFile(dir, "test", 0644); !errors.Is(err, os.ErrExist) {
		t.Errorf("openTempFile() did not fail with ErrExist: %v", err)
	}
}

func TestTempFile(t *testing.T) {
	withUmask(t, 0077)

	tmpdir := t.TempDir()
	pathNew := filepath.Join(tmpdir, "new.txt")
	pathExisting := filepath.Join(tmpdir, "existing.txt")

	if err := ioutil.WriteFile(pathExisting, []byte("content"), 0644); err != nil {
		t.Errorf("WriteFile(%q) failed: %v", pathExisting, err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{
			name: "new file",
			path: pathNew,
		},
		{
			name: "existing",
			path: pathExisting,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pf, err := TempFile("", tc.path)
			if err != nil {
				t.Errorf("TempFile(%q) failed: %v", tc.path, err)
			}

			t.Cleanup(func() {
				if err := pf.Cleanup(); err != nil {
					t.Errorf("Cleanup() failed: %v", err)
				}
			})

			if _, err := pf.WriteString("new content " + tc.name); err != nil {
				t.Errorf("Write() failed: %v", err)
			}

			if err := pf.CloseAtomicallyReplace(); err != nil {
				t.Errorf("CloseAtomicallyReplace() failed: %v", err)
			}

			if _, err := pf.Write(nil); !errors.Is(err, os.ErrClosed) {
				t.Errorf("Write() after CloseAtomicallyReplace didn't fail with ErrClosed: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		path     string
		want     string
		wantPerm os.FileMode
	}{
		{
			path:     pathNew,
			want:     "new content new file",
			wantPerm: 0600,
		},
		{
			path:     pathExisting,
			want:     "new content existing",
			wantPerm: 0600,
		},
	} {
		if got, err := ioutil.ReadFile(tc.path); err != nil {
			t.Errorf("ReadFile(%q) failed: %v", tc.path, err)
		} else if string(got) != tc.want {
			t.Errorf("Read unexpected content %q from %q, want %q", string(got), tc.path, tc.want)
		}

		if fi, err := os.Stat(tc.path); err != nil {
			t.Errorf("Stat(%q) failed: %v", tc.path, err)
		} else if got := fi.Mode() & os.ModePerm; got != tc.wantPerm {
			t.Errorf("%q has permissions 0%o, want 0%o", tc.path, got, tc.wantPerm)
		}
	}
}

func TestTempFileNoCommit(t *testing.T) {
	pathNew := filepath.Join(t.TempDir(), "new.txt")
	pathExisting := filepath.Join(t.TempDir(), "existing.txt")

	if err := ioutil.WriteFile(pathExisting, []byte("foobar"), 0644); err != nil {
		t.Errorf("WriteFile(%q) failed: %v", pathExisting, err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{
			name: "new file",
			path: pathNew,
		},
		{
			name: "existing",
			path: pathExisting,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pf, err := TempFile("", tc.path)
			if err != nil {
				t.Errorf("TempFile(%q) failed: %v", tc.path, err)
			}

			for i := 0; i < 3; i++ {
				if err := pf.Cleanup(); err != nil {
					t.Errorf("Cleanup() failed: %v", err)
				}
			}
		})
	}

	if _, err := os.Stat(pathNew); !os.IsNotExist(err) {
		t.Errorf("Stat(%q) didn't report that file doesn't exist: %v", pathNew, err)
	}

	if got, err := ioutil.ReadFile(pathExisting); err != nil {
		t.Errorf("ReadFile(%q) failed: %v", pathExisting, err)
	} else if want := "foobar"; string(got) != want {
		t.Errorf("Read unexpected content %q from %q, want %q", string(got), pathExisting, want)
	}
}