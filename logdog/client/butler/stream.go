// Copyright 2015 The LUCI Authors. All rights reserved.
// Use of this source code is governed under the Apache License, Version 2.0
// that can be found in the LICENSE file.

package butler

import (
	"io"

	"github.com/luci/luci-go/common/clock"
	"github.com/luci/luci-go/common/iotools"
	log "github.com/luci/luci-go/common/logging"
	"github.com/luci/luci-go/logdog/client/butler/bundler"
	"golang.org/x/net/context"
)

type stream struct {
	context.Context

	r           io.Reader
	c           io.Closer
	bs          bundler.Stream
	isKeepAlive bool
}

func (s *stream) readChunk() bool {
	d := s.bs.LeaseData()

	amount, err := s.r.Read(d.Bytes())
	if amount > 0 {
		d.Bind(amount, clock.Now(s))

		// Add the data to our bundler endpoint. This may block waiting for the
		// bundler to consume data.
		log.Fields{
			"numBytes": amount,
		}.Debugf(s, "Read byte(s).")

		err := s.bs.Append(d)
		d = nil // Append takes ownership of "d" regardless of error.
		if err != nil {
			log.Fields{
				log.ErrorKey: err,
			}.Errorf(s, "Failed to Append to stream.")
			return false
		}
	}

	// If we haven't consumed our data, release it.
	if d != nil {
		d.Release()
		d = nil
	}

	switch err {
	case iotools.ErrTimeout:
		log.Debugf(s, "Encountered 'Read()' timeout; re-reading.")
		fallthrough
	case nil:
		break

	case io.EOF:
		log.WithError(err).Debugf(s, "Stream encountered EOF.")
		return false

	default:
		log.WithError(err).Errorf(s, "Stream encountered error during Read.")
		return false
	}

	return true
}

func (s *stream) closeStream() {
	if err := s.c.Close(); err != nil {
		log.Fields{
			log.ErrorKey: err,
		}.Warningf(s, "Error closing stream.")
	}
	s.bs.Close()
}
