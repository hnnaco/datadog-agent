// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux

package checks

import (
	"testing"

	configmock "github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/util/flavor"
)

func TestProcessEventsCheckEnabled(t *testing.T) {
	deps := createDeps(t)
	t.Run("default", func(t *testing.T) {
		cfg := configmock.New(t)

		enabledChecks := getEnabledChecks(t, cfg, configmock.NewSystemProbe(t), deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertNotContainsCheck(t, enabledChecks, ProcessEventsCheckName)
	})

	t.Run("enabled", func(t *testing.T) {
		cfg := configmock.New(t)
		cfg.SetWithoutSource("process_config.event_collection.enabled", true)

		enabledChecks := getEnabledChecks(t, cfg, configmock.NewSystemProbe(t), deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertContainsCheck(t, enabledChecks, ProcessEventsCheckName)
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := configmock.New(t)
		cfg.SetWithoutSource("process_config.event_collection.enabled", false)

		enabledChecks := getEnabledChecks(t, cfg, configmock.NewSystemProbe(t), deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertNotContainsCheck(t, enabledChecks, ProcessEventsCheckName)
	})
}

func TestConnectionsCheckLinux(t *testing.T) {
	deps := createDeps(t)
	originalFlavor := flavor.GetFlavor()
	defer flavor.SetFlavor(originalFlavor)

	// Make sure the connections check is disabled on the core agent
	// and enabled in the process agent when process checks run in core agent
	t.Run("run in core agent", func(t *testing.T) {
		cfg, scfg := configmock.New(t), configmock.NewSystemProbe(t)
		cfg.SetWithoutSource("process_config.process_collection.enabled", true)
		cfg.SetWithoutSource("process_config.run_in_core_agent.enabled", true)
		scfg.SetWithoutSource("network_config.enabled", true)
		scfg.SetWithoutSource("system_probe_config.enabled", true)

		flavor.SetFlavor("agent")
		enabledChecks := getEnabledChecks(t, cfg, scfg, deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertNotContainsCheck(t, enabledChecks, ConnectionsCheckName)

		flavor.SetFlavor("process_agent")
		enabledChecks = getEnabledChecks(t, cfg, scfg, deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertContainsCheck(t, enabledChecks, ConnectionsCheckName)
	})
}

func TestProcessCheckLinux(t *testing.T) {
	deps := createDeps(t)
	originalFlavor := flavor.GetFlavor()
	defer flavor.SetFlavor(originalFlavor)

	// Make sure process checks run on the core agent only
	// when run in core agent mode is enabled
	t.Run("run in core agent", func(t *testing.T) {
		cfg, scfg := configmock.New(t), configmock.NewSystemProbe(t)
		cfg.SetWithoutSource("process_config.process_collection.enabled", true)
		cfg.SetWithoutSource("process_config.run_in_core_agent.enabled", true)

		flavor.SetFlavor("process_agent")
		enabledChecks := getEnabledChecks(t, cfg, scfg, deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertNotContainsCheck(t, enabledChecks, ProcessCheckName)

		flavor.SetFlavor("agent")
		enabledChecks = getEnabledChecks(t, cfg, scfg, deps.WMeta, deps.GpuSubscriber, deps.NpCollector, deps.Statsd)
		assertContainsCheck(t, enabledChecks, ProcessCheckName)
	})
}
