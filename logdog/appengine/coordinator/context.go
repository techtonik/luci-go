// Copyright 2015 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package coordinator

import (
	"fmt"

	"github.com/luci/gae/service/info"
	log "github.com/luci/luci-go/common/logging"
	"github.com/luci/luci-go/grpc/grpcutil"
	"github.com/luci/luci-go/logdog/api/config/svcconfig"
	"github.com/luci/luci-go/logdog/appengine/coordinator/config"
	"github.com/luci/luci-go/luci_config/common/cfgtypes"
	"github.com/luci/luci-go/luci_config/server/cfgclient"
	"github.com/luci/luci-go/server/auth"
	"github.com/luci/luci-go/server/auth/identity"

	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
)

// NamespaceAccessType specifies the type of namespace access that is being
// requested for WithProjectNamespace.
type NamespaceAccessType int

const (
	// NamespaceAccessNoAuth grants unconditional access to a project's namespace.
	// This bypasses all ACL checks, and must only be used by service endpoints
	// that explicitly apply ACLs elsewhere.
	NamespaceAccessNoAuth NamespaceAccessType = iota

	// NamespaceAccessAllTesting is an extension of NamespaceAccessNoAuth that,
	// in addition to doing no ACL checks, also does no project existence checks.
	//
	// This must ONLY be used for testing.
	NamespaceAccessAllTesting

	// NamespaceAccessREAD enforces READ permission access to a project's
	// namespace.
	NamespaceAccessREAD

	// NamespaceAccessWRITE enforces WRITE permission access to a project's
	// namespace.
	NamespaceAccessWRITE
)

type servicesKeyType int

// WithServices installs the supplied Services instance into a Context.
func WithServices(c context.Context, s Services) context.Context {
	return context.WithValue(c, servicesKeyType(0), s)
}

// GetServices gets the Services instance installed in the supplied Context.
//
// If no Services has been installed, it will panic.
func GetServices(c context.Context) Services {
	s, ok := c.Value(servicesKeyType(0)).(Services)
	if !ok {
		panic("no Services instance is installed")
	}
	return s
}

// WithProjectNamespace sets the current namespace to the project name.
//
// It will return a user-facing wrapped gRPC error on failure:
//	- InvalidArgument if the project name is invalid.
//	- If the project exists, then
//	  - nil, if the user has the requested access.
//	  - Unauthenticated if the user does not have the requested access, but is
//	    also not authenticated. This lets them know they should try again after
//	    authenticating.
//	  - PermissionDenied if the user does not have the requested access.
//	- PermissionDenied if the project doesn't exist.
//	- Internal if an internal error occurred.
func WithProjectNamespace(c *context.Context, project cfgtypes.ProjectName, at NamespaceAccessType) error {
	ctx := *c

	if err := project.Validate(); err != nil {
		log.WithError(err).Errorf(ctx, "Project name is invalid.")
		return grpcutil.Errf(codes.InvalidArgument, "Project name is invalid: %s", err)
	}

	// Return gRPC error for when the user is denied access and does not have READ
	// access. Returns either Unauthenticated if the user is not authenticated
	// or PermissionDenied if the user is authenticated.
	getAccessDeniedError := func() error {
		if id := auth.CurrentIdentity(ctx); id.Kind() == identity.Anonymous {
			return grpcutil.Unauthenticated
		}

		// Deny the existence of the project.
		return grpcutil.Errf(codes.PermissionDenied,
			"The project is invalid, or you do not have permission to access it.")
	}

	// Returns the project config, or "read denied" error if the project does not
	// exist.
	getProjectConfig := func() (*svcconfig.ProjectConfig, error) {
		pcfg, err := GetServices(ctx).ProjectConfig(ctx, project)
		switch err {
		case nil:
			// Successfully loaded project config.
			return pcfg, nil

		case cfgclient.ErrNoConfig, config.ErrInvalidConfig:
			// If the configuration request was valid, but no configuration could be
			// loaded, treat this as the user not having READ access to the project.
			// Otherwise, the user could use this error response to confirm a
			// project's existence.
			log.Fields{
				log.ErrorKey: err,
				"project":    project,
			}.Errorf(ctx, "Could not load config for project.")
			return nil, getAccessDeniedError()

		default:
			// The configuration attempt failed to load. This is an internal error,
			// and is safe to return because it's not contingent on the existence (or
			// lack thereof) of the project.
			return nil, grpcutil.Internal
		}
	}

	// Validate that the current user has the requested access.
	switch at {
	case NamespaceAccessNoAuth:
		// Assert that the project exists and has a configuration.
		if _, err := getProjectConfig(); err != nil {
			return err
		}

	case NamespaceAccessAllTesting:
		// Sanity check: this should only be used on development instances.
		if !info.IsDevAppServer(ctx) {
			panic("Testing access requested on non-development instance.")
		}
		break

	case NamespaceAccessREAD:
		// Assert that the current user has READ access.
		pcfg, err := getProjectConfig()
		if err != nil {
			return err
		}

		if err := IsProjectReader(*c, pcfg); err != nil {
			log.WithError(err).Errorf(*c, "User denied READ access to requested project.")
			return getAccessDeniedError()
		}

	case NamespaceAccessWRITE:
		// Assert that the current user has WRITE access.
		pcfg, err := getProjectConfig()
		if err != nil {
			return err
		}

		if err := IsProjectWriter(*c, pcfg); err != nil {
			log.WithError(err).Errorf(*c, "User denied WRITE access to requested project.")
			return getAccessDeniedError()
		}

	default:
		panic(fmt.Errorf("unknown access type: %v", at))
	}

	pns := ProjectNamespace(project)
	nc, err := info.Namespace(ctx, pns)
	if err != nil {
		log.Fields{
			log.ErrorKey: err,
			"project":    project,
			"namespace":  pns,
		}.Errorf(ctx, "Failed to set namespace.")
		return grpcutil.Internal
	}

	*c = nc
	return nil
}

// Project returns the current project installed in the supplied Context's
// namespace.
//
// This function is called with the expectation that the Context is in a
// namespace conforming to ProjectNamespace. If this is not the case, this
// method will panic.
func Project(c context.Context) cfgtypes.ProjectName {
	ns := info.GetNamespace(c)
	project := ProjectFromNamespace(ns)
	if project != "" {
		return project
	}
	panic(fmt.Errorf("current namespace %q does not begin with project namespace prefix (%q)", ns, projectNamespacePrefix))
}
