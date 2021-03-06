// Copyright 2016 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package authtest

import (
	"net/http"
	"time"

	"golang.org/x/net/context"
	"golang.org/x/oauth2"

	"github.com/luci/luci-go/common/clock"
	"github.com/luci/luci-go/server/auth"
)

// MockAuthConfig configures auth library for unit tests environment.
//
// If modifies the configure stored in the context. See auth.SetConfig for more
// info.
func MockAuthConfig(c context.Context) context.Context {
	return auth.ModifyConfig(c, func(cfg auth.Config) auth.Config {
		cfg.AnonymousTransport = func(context.Context) http.RoundTripper {
			return http.DefaultTransport
		}
		cfg.AccessTokenProvider = func(ic context.Context, scopes []string) (*oauth2.Token, error) {
			return &oauth2.Token{
				AccessToken: "fake_token",
				TokenType:   "Bearer",
				Expiry:      clock.Now(ic).Add(time.Hour).UTC(),
			}, nil
		}
		return cfg
	})
}
