// Copyright 2015 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package log

import (
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/golang/protobuf/proto"
	"github.com/luci/luci-go/client/internal/logdog/butler/output"
	"github.com/luci/luci-go/common/logdog/protocol"
	"github.com/luci/luci-go/common/logdog/types"
	log "github.com/luci/luci-go/common/logging"
	"golang.org/x/net/context"
)

// logOutput is an Output implementation that logs messages to its contexts'
// Logger instance.
type logOutput struct {
	sync.Mutex
	ctx context.Context

	// bundleSize is the maximum size of the Butler bundle to use.
	bundleSize int

	stats output.StatsBase
}

// logOutput implements output.Output.
var _ output.Output = (*logOutput)(nil)

// New instantes a new log output instance.
func New(ctx context.Context, bundleSize int) output.Output {
	o := logOutput{
		ctx:        ctx,
		bundleSize: bundleSize,
	}
	o.ctx = log.SetFields(o.ctx, log.Fields{
		"output": &o,
	})
	return &o
}

func (o *logOutput) SendBundle(bundle *protocol.ButlerLogBundle) error {
	o.Lock()
	defer o.Unlock()

	for _, e := range bundle.Entries {
		path := types.StreamName(e.Desc.Prefix).Join(types.StreamName(e.Desc.Name))
		ctx := log.SetField(o.ctx, "streamPath", path)

		log.Fields{
			"count":      len(e.Logs),
			"descriptor": e.Desc.String(),
		}.Infof(ctx, "Received stream logs.")

		for _, le := range e.Logs {
			log.Fields{
				"timeOffset":  le.TimeOffset.Duration(),
				"prefixIndex": le.PrefixIndex,
				"streamIndex": le.StreamIndex,
				"sequence":    le.Sequence,
			}.Infof(ctx, "Received message.")
			if c := le.GetText(); c != nil {
				for idx, l := range c.Lines {
					log.Infof(ctx, "Line %d) %s (%s)", idx, l.Value, strconv.Quote(l.Delimiter))
				}
			}
			if c := le.GetBinary(); c != nil {
				log.Infof(ctx, "Binary) %s", hex.EncodeToString(c.Data))
			}
			if c := le.GetDatagram(); c != nil {
				if cp := c.Partial; cp != nil {
					log.Infof(ctx, "Datagram (%#v) (%d bytes): %s", cp, cp.Size, hex.EncodeToString(c.Data))
				} else {
					log.Infof(ctx, "Datagram (%d bytes): %s", len(c.Data), hex.EncodeToString(c.Data))
				}
			}

			o.stats.F.SentMessages++
		}
	}
	o.stats.F.SentBytes += proto.Size(bundle)

	return nil
}

func (o *logOutput) MaxSize() int {
	return o.bundleSize
}

func (o *logOutput) Stats() output.Stats {
	o.Lock()
	defer o.Unlock()

	st := o.stats
	return &st
}

func (o *logOutput) Close() {}