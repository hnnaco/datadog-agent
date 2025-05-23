// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package sysprobeconfig implements a component to handle system-probe configuration.  This
// component temporarily wraps pkg/config.
//
// This component initializes pkg/config based on the bundle params, and
// will return the same results as that package.  This is to support migration
// to a component architecture.  When no code still uses pkg/config, that
// package will be removed.
//
// The mock component does nothing at startup, beginning with an empty config.
// It also overwrites the pkg/config.SystemProbe for the duration of the test.
package sysprobeconfig

import (
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/pkg/config/model"
	sysconfigtypes "github.com/DataDog/datadog-agent/pkg/system-probe/config/types"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

// team: ebpf-platform

// Component is the component type.
type Component interface {
	model.ReaderWriter

	// Warnings returns config warnings collected during setup.
	Warnings() *model.Warnings

	// SysProbeObject returns the wrapper sysconfig
	SysProbeObject() *sysconfigtypes.Config
}

// NoneModule return a None optional type for sysprobeconfig.Component.
//
// This helper allows code that needs a disabled Optional type for sysprobeconfig to get it. The helper is split from
// the implementation to avoid linking with the dependencies from sysprobeconfig.
func NoneModule() fxutil.Module {
	return fxutil.Component(fx.Provide(func() option.Option[Component] {
		return option.None[Component]()
	}))
}
