// Copyright 2016 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package delegation

import (
	"fmt"
	"strings"

	"github.com/luci/luci-go/common/config/validation"
	"github.com/luci/luci-go/common/data/stringset"
	"github.com/luci/luci-go/server/auth/identity"

	"github.com/luci/luci-go/tokenserver/api/admin/v1"
	"github.com/luci/luci-go/tokenserver/appengine/impl/utils/policy"
)

// validateConfigs validates the structure of configs fetched by fetchConfigs.
func validateConfigs(bundle policy.ConfigBundle, ctx *validation.Context) {
	ctx.SetFile(delegationCfg)
	cfg, ok := bundle[delegationCfg].(*admin.DelegationPermissions)
	if !ok {
		ctx.Error("unexpectedly wrong proto type %T", cfg)
		return
	}

	names := stringset.New(0)
	for i, rule := range cfg.Rules {
		if rule.Name != "" {
			if names.Has(rule.Name) {
				ctx.Error("two rules with identical name %q", rule.Name)
			}
			names.Add(rule.Name)
		}
		validateRule(fmt.Sprintf("rule #%d: %q", i+1, rule.Name), rule, ctx)
	}
}

// validateRule checks single DelegationRule proto.
//
// See config.proto, DelegationRule for the description of allowed values.
func validateRule(title string, r *admin.DelegationRule, ctx *validation.Context) {
	ctx.Enter(title)
	defer ctx.Exit()

	if r.Name == "" {
		ctx.Error(`"name" is required`)
	}

	v := identitySetValidator{
		Field:       "requestor",
		Context:     ctx,
		AllowGroups: true,
	}
	v.validate(r.Requestor)

	v = identitySetValidator{
		Field:              "allowed_to_impersonate",
		Context:            ctx,
		AllowReservedWords: []string{Requestor}, // '*' is not allowed here though
		AllowGroups:        true,
	}
	v.validate(r.AllowedToImpersonate)

	v = identitySetValidator{
		Field:              "allowed_audience",
		Context:            ctx,
		AllowReservedWords: []string{Requestor, "*"},
		AllowGroups:        true,
	}
	v.validate(r.AllowedAudience)

	v = identitySetValidator{
		Field:              "target_service",
		Context:            ctx,
		AllowReservedWords: []string{"*"},
		AllowIDKinds:       []identity.Kind{identity.Service},
	}
	v.validate(r.TargetService)

	switch {
	case r.MaxValidityDuration == 0:
		ctx.Error(`"max_validity_duration" is required`)
	case r.MaxValidityDuration < 0:
		ctx.Error(`"max_validity_duration" must be positive`)
	case r.MaxValidityDuration > 24*3600:
		ctx.Error(`"max_validity_duration" must be smaller than 86401`)
	}
}

type identitySetValidator struct {
	Field              string              // name of the field being validated
	Context            *validation.Context // where to emit errors to
	AllowReservedWords []string            // to allow "*" and "REQUESTOR"
	AllowGroups        bool                // true to allow "group:" entries
	AllowIDKinds       []identity.Kind     // permitted identity kinds, or nil if all
}

func (v *identitySetValidator) validate(items []string) {
	if len(items) == 0 {
		v.Context.Error("%q is required", v.Field)
		return
	}

	v.Context.Enter("%q", v.Field)
	defer v.Context.Exit()

loop:
	for _, s := range items {
		// A reserved word?
		for _, r := range v.AllowReservedWords {
			if s == r {
				continue loop
			}
		}

		// A group reference?
		if strings.HasPrefix(s, "group:") {
			if !v.AllowGroups {
				v.Context.Error("group entries are not allowed - %q", s)
			} else {
				if s == "group:" {
					v.Context.Error("bad group entry %q", s)
				}
			}
			continue
		}

		// An identity then.
		id, err := identity.MakeIdentity(s)
		if err != nil {
			v.Context.Error("%s", err)
			continue
		}

		if v.AllowIDKinds != nil {
			allowed := false
			for _, k := range v.AllowIDKinds {
				if id.Kind() == k {
					allowed = true
					break
				}
			}
			if !allowed {
				v.Context.Error("identity of kind %q is not allowed here - %q", id.Kind(), s)
			}
		}
	}
}
