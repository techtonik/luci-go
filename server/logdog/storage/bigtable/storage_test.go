// Copyright 2015 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package bigtable

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/luci/gkvlite"
	"github.com/luci/luci-go/common/logdog/types"
	"github.com/luci/luci-go/common/recordio"
	"github.com/luci/luci-go/server/logdog/storage"
	"golang.org/x/net/context"
	"google.golang.org/cloud/bigtable"

	. "github.com/luci/luci-go/common/testing/assertions"
	. "github.com/smartystreets/goconvey/convey"
)

// btTableTest is an in-memory implementation of btTable interface for testing.
//
// This is a simple implementation; not an efficient one.
type btTableTest struct {
	s *gkvlite.Store
	c *gkvlite.Collection

	// err, if true, is the error immediately returned by functions.
	err error

	// maxLogAge is the currently-configured maximum log age.
	maxLogAge time.Duration
}

func (t *btTableTest) close() {
	if t.s != nil {
		t.s.Close()
		t.s = nil
	}
}

func (t *btTableTest) collection() *gkvlite.Collection {
	if t.s == nil {
		var err error
		t.s, err = gkvlite.NewStore(nil)
		if err != nil {
			panic(err)
		}
		t.c = t.s.MakePrivateCollection(bytes.Compare)
	}
	return t.c
}

func (t *btTableTest) putLogData(c context.Context, rk *rowKey, d []byte) error {
	if t.err != nil {
		return t.err
	}

	enc := []byte(rk.encode())
	coll := t.collection()
	if item, _ := coll.Get(enc); item != nil {
		return storage.ErrExists
	}

	clone := make([]byte, len(d))
	copy(clone, d)
	if err := coll.Set(enc, clone); err != nil {
		panic(err)
	}
	return nil
}

func (t *btTableTest) getLogData(c context.Context, rk *rowKey, limit int, keysOnly bool, cb btGetCallback) error {
	if t.err != nil {
		return t.err
	}

	enc := []byte(rk.encode())
	prefix := rk.pathPrefix()
	var ierr error
	err := t.collection().VisitItemsAscend(enc, !keysOnly, func(i *gkvlite.Item) bool {
		var drk *rowKey
		drk, ierr = decodeRowKey(string(i.Key))
		if ierr != nil {
			return false
		}
		if drk.pathPrefix() != prefix {
			return false
		}

		if ierr = cb(drk, i.Val); ierr != nil {
			if ierr == errStop {
				ierr = nil
			}
			return false
		}

		if limit > 0 {
			limit--
			if limit == 0 {
				return false
			}
		}

		return true
	})
	if err != nil {
		panic(err)
	}
	return ierr
}

func (t *btTableTest) setMaxLogAge(c context.Context, d time.Duration) error {
	if t.err != nil {
		return t.err
	}
	t.maxLogAge = d
	return nil
}

func (t *btTableTest) dataMap() map[string][]byte {
	result := map[string][]byte{}

	err := t.collection().VisitItemsAscend([]byte(nil), true, func(i *gkvlite.Item) bool {
		result[string(i.Key)] = i.Val
		return true
	})
	if err != nil {
		panic(err)
	}
	return result
}

func TestStorage(t *testing.T) {
	t.Parallel()

	Convey(`A BigTable storage instance bound to a testing BigTable instance`, t, func() {
		bt := btTableTest{}
		defer bt.close()

		s := newBTStorage(context.Background(), Options{
			Project:  "test-project",
			Zone:     "test-zone",
			Cluster:  "test-cluster",
			LogTable: "test-log-table",
		}, nil, nil)

		s.raw = &bt
		defer s.Close()

		get := func(path string, index int, limit int) ([]string, error) {
			req := storage.GetRequest{
				Path:  types.StreamPath(path),
				Index: types.MessageIndex(index),
				Limit: limit,
			}
			got := []string{}
			err := s.Get(req, func(idx types.MessageIndex, d []byte) bool {
				got = append(got, string(d))
				return true
			})
			return got, err
		}

		put := func(path string, index int, d ...string) error {
			data := make([][]byte, len(d))
			for i, v := range d {
				data[i] = []byte(v)
			}

			return s.Put(storage.PutRequest{
				Path:   types.StreamPath(path),
				Index:  types.MessageIndex(index),
				Values: data,
			})
		}

		ekey := func(p string, v int64) string {
			return newRowKey(p, v).encode()
		}
		records := func(s ...string) []byte {
			buf := bytes.Buffer{}
			w := recordio.NewWriter(&buf)

			for _, v := range s {
				if _, err := w.Write([]byte(v)); err != nil {
					panic(err)
				}
				if err := w.Flush(); err != nil {
					panic(err)
				}
			}

			return buf.Bytes()
		}

		Convey(`With an artificial maximum BigTable row size of two records`, func() {
			// Artificially constrain row size. 4 = 2*{size/1, data/1} RecordIO
			// entries.
			s.maxRowSize = 4

			Convey(`Will split row data that overflows the table into multiple rows.`, func() {
				So(put("A", 0, "0", "1", "2", "3"), ShouldBeNil)

				So(bt.dataMap(), ShouldResemble, map[string][]byte{
					ekey("A", 1): records("0", "1"),
					ekey("A", 3): records("2", "3"),
				})
			})

			Convey(`Loading a single row data beyond the maximum row size will fail.`, func() {
				So(put("A", 0, "0123"), ShouldErrLike, "single row entry exceeds maximum size")
			})
		})

		Convey(`With row data: A{0, 1, 2, 3, 4}, B{10, 12, 13}`, func() {
			So(put("A", 0, "0", "1", "2"), ShouldBeNil)
			So(put("A", 3, "3", "4"), ShouldBeNil)
			So(put("B", 10, "10"), ShouldBeNil)
			So(put("B", 12, "12", "13"), ShouldBeNil)

			Convey(`Testing "Put"...`, func() {
				Convey(`Loads the row data.`, func() {
					So(bt.dataMap(), ShouldResemble, map[string][]byte{
						ekey("A", 2):  records("0", "1", "2"),
						ekey("A", 4):  records("3", "4"),
						ekey("B", 10): records("10"),
						ekey("B", 13): records("12", "13"),
					})
				})
			})

			Convey(`Testing "Get"...`, func() {
				Convey(`Can fetch the full row, "A".`, func() {
					got, err := get("A", 0, 0)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{"0", "1", "2", "3", "4"})
				})

				Convey(`Will fetch A{1, 2, 3, 4} with when index=1.`, func() {
					got, err := get("A", 1, 0)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{"1", "2", "3", "4"})
				})

				Convey(`Will fetch A{1, 2} with when index=1 and limit=2.`, func() {
					got, err := get("A", 1, 2)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{"1", "2"})
				})

				Convey(`Will fetch B{10, 12, 13} for B.`, func() {
					got, err := get("B", 0, 0)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{"10", "12", "13"})
				})

				Convey(`Will fetch B{12, 13} when index=11.`, func() {
					got, err := get("B", 11, 0)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{"12", "13"})
				})

				Convey(`Will fetch {} for INVALID.`, func() {
					got, err := get("INVALID", 0, 0)
					So(err, ShouldBeNil)
					So(got, ShouldResemble, []string{})
				})
			})

			Convey(`Testing "Tail"...`, func() {
				tail := func(path string) (string, error) {
					got, _, err := s.Tail(types.StreamPath(path))
					return string(got), err
				}

				Convey(`A tail request for "A" returns A{4}.`, func() {
					got, err := tail("A")
					So(err, ShouldBeNil)
					So(got, ShouldEqual, "4")
				})

				Convey(`A tail request for "B" returns B{13}.`, func() {
					got, err := tail("B")
					So(err, ShouldBeNil)
					So(got, ShouldEqual, "13")
				})

				Convey(`A tail request for "INVALID" errors NOT FOUND.`, func() {
					_, err := tail("INVALID")
					So(err, ShouldEqual, storage.ErrDoesNotExist)
				})
			})
		})

		Convey(`Given a fake BigTable row`, func() {
			fakeRow := bigtable.Row{
				"log": []bigtable.ReadItem{
					{
						Row:    "testrow",
						Column: "log:data",
						Value:  []byte("here is my data"),
					},
				},
			}

			Convey(`Can extract log data.`, func() {
				d, err := getLogData(fakeRow)
				So(err, ShouldBeNil)
				So(d, ShouldResemble, []byte("here is my data"))
			})

			Convey(`Will fail to extract if the column is missing.`, func() {
				fakeRow["log"][0].Column = "not-data"
				_, err := getLogData(fakeRow)
				So(err, ShouldEqual, storage.ErrDoesNotExist)
			})

			Convey(`Will fail to extract if the family does not exist.`, func() {
				So(getReadItem(fakeRow, "invalid", "invalid"), ShouldBeNil)
			})

			Convey(`Will fail to extract if the column does not exist.`, func() {
				So(getReadItem(fakeRow, "log", "invalid"), ShouldBeNil)
			})
		})

		Convey(`When pushing a configuration`, func() {
			cfg := storage.Config{
				MaxLogAge: 1 * time.Hour,
			}

			Convey(`Can successfully apply configuration.`, func() {
				So(s.Config(cfg), ShouldBeNil)
				So(bt.maxLogAge, ShouldEqual, cfg.MaxLogAge)
			})

			Convey(`With return an error if the configuration fails to apply.`, func() {
				bt.err = errors.New("test error")

				So(s.Config(cfg), ShouldEqual, bt.err)
			})
		})
	})
}
