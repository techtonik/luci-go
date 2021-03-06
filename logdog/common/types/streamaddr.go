// Copyright 2017 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package types

import (
	"net/url"
	"strings"

	"github.com/luci/luci-go/common/errors"
	"github.com/luci/luci-go/luci_config/common/cfgtypes"
)

const logDogURLScheme = "logdog"

// StreamAddr is a fully-qualified LogDog stream address.
type StreamAddr struct {
	// Host is the LogDog host.
	Host string

	// Project is the LUCI project name that this log belongs to.
	Project cfgtypes.ProjectName

	// Path is the LogDog stream path.
	Path StreamPath
}

// String returns a string representation of this address.
func (s *StreamAddr) String() string { return s.URL().String() }

// URL returns a LogDog URL that represents this Stream.
func (s *StreamAddr) URL() *url.URL {
	return &url.URL{
		Scheme: logDogURLScheme,
		Host:   s.Host,
		Path:   strings.Join([]string{"", string(s.Project), string(s.Path)}, "/"),
	}
}

// ParseURL parses a LogDog URL into a Stream. If the URL is malformed, or
// if the host, project, or path is invalid, an error will be returned.
//
// A LogDog URL has the form:
// logdog://<host>/<project>/<prefix>/+/<name>
func ParseURL(v string) (*StreamAddr, error) {
	u, err := url.Parse(v)
	if err != nil {
		return nil, errors.Annotate(err).Reason("failed to parse URL").Err()
	}

	// Validate Scheme.
	if u.Scheme != logDogURLScheme {
		return nil, errors.Reason("URL scheme %(scheme)q is not "+logDogURLScheme).
			D("scheme", u.Scheme).
			Err()
	}
	addr := StreamAddr{
		Host: u.Host,
	}

	parts := strings.SplitN(u.Path, "/", 3)
	if len(parts) != 3 || len(parts[0]) != 0 {
		return nil, errors.Reason("URL path does not include both project and path components: %(path)s").
			D("path", u.Path).
			Err()
	}

	addr.Project, addr.Path = cfgtypes.ProjectName(parts[1]), StreamPath(parts[2])
	if err := addr.Project.Validate(); err != nil {
		return nil, errors.Annotate(err).Reason("invalid project name: %(project)q").
			D("project", addr.Project).
			Err()
	}

	if err := addr.Path.Validate(); err != nil {
		return nil, errors.Annotate(err).Reason("invalid stream path: %(path)q").
			D("path", addr.Path).
			Err()
	}

	return &addr, nil
}
