// Copyright 2016 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package common

import (
	"errors"
	"strings"
	"testing"

	"github.com/luci/gae/impl/memory"
	memcfg "github.com/luci/luci-go/common/config/impl/memory"
	"github.com/luci/luci-go/common/logging/gologger"
	"github.com/luci/luci-go/luci_config/server/cfgclient/backend/testconfig"

	"golang.org/x/net/context"

	. "github.com/smartystreets/goconvey/convey"
)

func TestConfig(t *testing.T) {
	t.Parallel()

	Convey("Test Environment", t, func() {
		c := memory.UseWithAppID(context.Background(), "dev~luci-milo")
		c = gologger.StdConfig.Use(c)

		Convey("Tests about global configs", func() {
			Convey("Read a config before anything is set", func() {
				c = testconfig.WithCommonClient(c, memcfg.New(mockedConfigs))
				err := UpdateServiceConfig(c)
				So(err, ShouldResemble, errors.New("could not load settings.cfg from luci-config: no such config"))
				settings := GetSettings(c)
				So(settings.Buildbot.InternalReader, ShouldEqual, "")
			})
			Convey("Read a config", func() {
				mockedConfigs["services/luci-milo"] = memcfg.ConfigSet{
					"settings.cfg": settingsCfg,
				}
				c = testconfig.WithCommonClient(c, memcfg.New(mockedConfigs))
				err := UpdateServiceConfig(c)
				So(err, ShouldBeNil)
				settings := GetSettings(c)
				So(settings.Buildbot.InternalReader, ShouldEqual, "googlers")
			})
		})

		Convey("Send update", func() {
			c = testconfig.WithCommonClient(c, memcfg.New(mockedConfigs))
			err := UpdateServiceConfig(c)
			So(err, ShouldBeNil)
			// Send update here
			err = UpdateProjectConfigs(c)
			So(err, ShouldBeNil)

			Convey("Check Project config updated", func() {
				p, err := GetProject(c, "foo")
				So(err, ShouldBeNil)
				So(p.ID, ShouldEqual, "foo")
				So(p.Readers, ShouldResemble, []string{"public", "foo@bar.com"})
				So(p.Writers, ShouldResemble, []string(nil))
			})

			Convey("Check Console config updated", func() {
				cs, err := GetConsole(c, "foo", "default")
				So(err, ShouldBeNil)
				So(cs.Name, ShouldEqual, "default")
				So(cs.RepoURL, ShouldEqual, "https://chromium.googlesource.com/foo/bar")
			})
		})

		Convey("Reject duplicate configs.", func() {
			c = testconfig.WithCommonClient(c, memcfg.New(mockedConfigs))
			err := UpdateServiceConfig(c)
			So(err, ShouldBeNil)
			mockedConfigs["projects/bar.git"] = memcfg.ConfigSet{"luci-milo.cfg": barCfg}

			err = UpdateProjectConfigs(c)
			So(strings.HasPrefix(err.Error(), "Duplicate project ID"), ShouldEqual, true)
		})
	})
}

var fooCfg = `
ID: "foo"
Readers: "public"
Readers: "foo@bar.com"
Consoles: {
	Name: "default"
	RepoURL: "https://chromium.googlesource.com/foo/bar"
	Branch: "master"
	Builders: {
		Module: "buildbucket"
		Name: "luci.foo.something"
		Category: "main|something"
		ShortName: "s"
	}
	Builders: {
		Module: "buildbucket"
		Name: "luci.foo.other"
		Category: "main|other"
		ShortName: "o"
	}
}
`

var barCfg = `
ID: "foo"
`

var settingsCfg = `
buildbot: {
	internal_reader: "googlers"
}
`

var mockedConfigs = map[string]memcfg.ConfigSet{
	"projects/foo.git": {
		"luci-milo.cfg": fooCfg,
	},
}
