// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

// Package probe holds probe related files
package probe

import (
	"encoding/binary"
	"github.com/DataDog/datadog-agent/pkg/security/resolvers/tags"
	"github.com/DataDog/datadog-go/v5/statsd"
)

// Opts defines some probe options
type Opts struct {
	// DontDiscardRuntime do not discard the runtime. Mostly used by functional tests
	DontDiscardRuntime bool
	// StatsdClient to be used for probe stats
	StatsdClient statsd.ClientInterface
	// PathResolutionEnabled defines if the path resolution is enabled
	PathResolutionEnabled bool
	// EnvsVarResolutionEnabled defines if environment variables resolution is enabled
	EnvsVarResolutionEnabled bool
	// Tagger will override the default one. Mainly here for tests.
	Tagger tags.Tagger
	// SyscallsMonitorEnabled enable syscalls map monitor
	SyscallsMonitorEnabled bool
	// TTYFallbackEnabled enable the tty procfs fallback
	TTYFallbackEnabled bool
	// EBPFLessEnabled use ebpfless source
	EBPFLessEnabled bool
	// DNSPort allows to change the DNS port where the events are captured from
	DNSPort uint16
}

func (o *Opts) normalize() {
	if o.StatsdClient == nil {
		o.StatsdClient = &statsd.NoOpClient{}
	}

	if o.DNSPort == 0 {
		o.DNSPort = 53
	}

	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, o.DNSPort)
	o.DNSPort = binary.BigEndian.Uint16(b)
}
