// Copyright 2015 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package deps

import (
	"testing"

	"github.com/luci/gae/impl/memory"
	"github.com/luci/gae/service/datastore"
	"github.com/luci/luci-go/appengine/cmd/dm/model"
	"github.com/luci/luci-go/common/api/dm/service/v1"
	. "github.com/luci/luci-go/common/testing/assertions"
	. "github.com/smartystreets/goconvey/convey"
	"golang.org/x/net/context"
)

func TestEnsureAttempt(t *testing.T) {
	t.Parallel()

	Convey("EnsureAttempt", t, func() {
		c := memory.Use(context.Background())
		ds := datastore.Get(c)
		s := &deps{}

		Convey("bad", func() {
			Convey("no quest", func() {
				_, err := s.EnsureAttempt(c, &dm.EnsureAttemptReq{ToEnsure: dm.NewAttemptID("quest", 1)})
				So(err, ShouldErrLike, "no such quest")
			})
		})

		Convey("good", func() {
			So(ds.Put(&model.Quest{ID: "quest"}), ShouldBeNil)

			_, err := s.EnsureAttempt(c, &dm.EnsureAttemptReq{ToEnsure: dm.NewAttemptID("quest", 1)})
			So(err, ShouldBeNil)

			So(ds.Get(&model.Attempt{ID: *dm.NewAttemptID("quest", 1)}), ShouldBeNil)
		})

	})
}