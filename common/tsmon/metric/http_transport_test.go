// Copyright 2016 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package metric

import (
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/luci/luci-go/common/clock/testclock"
	"github.com/luci/luci-go/common/tsmon"

	. "github.com/smartystreets/goconvey/convey"
)

type fakeRoundTripper struct {
	tc       testclock.TestClock
	duration time.Duration

	resp http.Response
	err  error
}

func (t *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.tc.Add(t.duration)
	io.Copy(ioutil.Discard, req.Body)
	return &t.resp, t.err
}

func TestHTTPRoundTripper(t *testing.T) {
	t.Parallel()

	Convey("With a fake round tripper and dummy state", t, func() {
		ctx, tc := testclock.UseTime(context.Background(), testclock.TestTimeUTC)
		ctx, _ = tsmon.WithDummyInMemory(ctx)

		rt := &fakeRoundTripper{tc: tc}
		c := http.Client{Transport: InstrumentTransport(ctx, rt, "foo")}

		Convey("successful request", func() {
			rt.duration = time.Millisecond * 42
			rt.resp.StatusCode = 200
			rt.resp.ContentLength = 8

			req, _ := http.NewRequest("GET", "https://www.example.com", strings.NewReader("12345"))
			req = req.WithContext(ctx)

			resp, err := c.Do(req)
			So(err, ShouldBeNil)
			So(resp.StatusCode, ShouldEqual, 200)

			v, err := requestBytesMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 5)
			v, err = responseBytesMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 8)
			v, err = requestDurationsMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 42)
			iv, err := responseStatusMetric.Get(ctx, 200, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(iv, ShouldEqual, 1)
		})

		Convey("error with no response", func() {
			rt.duration = time.Millisecond * 42
			rt.err = errors.New("oops")

			req, _ := http.NewRequest("GET", "https://www.example.com", strings.NewReader("12345"))
			req = req.WithContext(ctx)

			resp, err := c.Do(req)
			So(err, ShouldNotBeNil)
			So(resp, ShouldBeNil)

			v, err := requestBytesMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 5)
			v, err = responseBytesMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 0)
			v, err = requestDurationsMetric.Get(ctx, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(v.Count(), ShouldEqual, 1)
			So(v.Sum(), ShouldEqual, 42)
			iv, err := responseStatusMetric.Get(ctx, 0, "www.example.com", "foo")
			So(err, ShouldBeNil)
			So(iv, ShouldEqual, 1)
		})
	})
}
