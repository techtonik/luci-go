// Copyright 2016 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package tsmon

import (
	"fmt"
	"time"

	"golang.org/x/net/context"

	ds "github.com/luci/gae/service/datastore"
	"github.com/luci/gae/service/info"
	"github.com/luci/luci-go/common/clock"
)

const (
	// targetDataCenter is the value set on the "data_center" field in the
	// ts_mon.proto.Task message.
	targetDataCenter = "appengine"

	// instanceNamespace is the namespace to use for datastore instances.
	instanceNamespace = "ts_mon_instance_namespace"

	// prodXEndpoint is endpoint to send metrics to.
	prodXEndpoint = "https://prodxmon-pa.googleapis.com/v1:insert"

	instanceExpirationTimeout     = 30 * time.Minute
	instanceExpectedToHaveTaskNum = 5 * time.Minute
	flushTimeout                  = 5 * time.Second
)

type instance struct {
	_kind       string    `gae:"$kind,Instance"`
	ID          string    `gae:"$id"`
	TaskNum     int       `gae:"task_num"`     // Field names should match Python
	LastUpdated time.Time `gae:"last_updated"` // implementation.
}

// instanceEntityID returns a string unique to this appengine module, version
// and instance, to be used as the datastore ID for an "instance" entity.
func instanceEntityID(c context.Context) string {
	return fmt.Sprintf("%s.%s.%s", info.InstanceID(c), info.VersionID(c), info.ModuleName(c))
}

// getOrCreateInstanceEntity returns the instance entity for this appengine
// instance, adding a default one to the datastore if it doesn't exist.
//
// We need to register an entity ASAP to allow housekeepingHandler to
// discover the new instance.
func getOrCreateInstanceEntity(c context.Context) (*instance, error) {
	entity := instance{
		ID:          instanceEntityID(c),
		TaskNum:     -1,
		LastUpdated: clock.Get(c).Now().UTC(),
	}
	err := ds.Get(c, &entity)
	if err == ds.ErrNoSuchEntity {
		err = ds.RunInTransaction(c, func(c context.Context) error {
			switch err := ds.Get(c, &entity); err {
			case nil:
				return nil
			case ds.ErrNoSuchEntity:
				// Insert it into datastore if it didn't exist.
				return ds.Put(c, &entity)
			default:
				return err
			}
		}, nil)
	}
	return &entity, err
}

// refreshLastUpdatedTime updates LastUpdated field in the instance entity.
//
// It does it in a transaction to avoid overwriting TaskNum.
func refreshLastUpdatedTime(c context.Context, t time.Time) error {
	entity := instance{ID: instanceEntityID(c)}
	return ds.RunInTransaction(c, func(c context.Context) error {
		if err := ds.Get(c, &entity); err != nil {
			return err
		}
		entity.LastUpdated = t.UTC()
		return ds.Put(c, &entity)
	}, nil)
}
