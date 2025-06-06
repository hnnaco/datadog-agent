// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package securityagentimpl implements the securityagent metadata providers interface
package securityagentimpl

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/comp/core/config"
	ipc "github.com/DataDog/datadog-agent/comp/core/ipc/def"
	ipcmock "github.com/DataDog/datadog-agent/comp/core/ipc/mock"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	logmock "github.com/DataDog/datadog-agent/comp/core/log/mock"
	configFetcher "github.com/DataDog/datadog-agent/pkg/config/fetcher"
	"github.com/DataDog/datadog-agent/pkg/config/model"
	serializermock "github.com/DataDog/datadog-agent/pkg/serializer/mocks"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/version"
)

func setupFetcher(t *testing.T) {
	t.Cleanup(func() {
		fetchSecurityAgentConfig = configFetcher.SecurityAgentConfig
		fetchSecurityAgentConfigBySource = configFetcher.SecurityAgentConfigBySource
	})

	fetchSecurityAgentConfig = func(_ model.Reader) (string, error) { return "full config", nil }
	fetchSecurityAgentConfigBySource = func(_ model.Reader) (string, error) {
		data, err := json.Marshal(map[string]interface{}{
			string(model.SourceFile):               map[string]bool{"file": true},
			string(model.SourceEnvVar):             map[string]bool{"env": true},
			string(model.SourceAgentRuntime):       map[string]bool{"runtime": true},
			string(model.SourceLocalConfigProcess): map[string]bool{"local": true},
			string(model.SourceRC):                 map[string]bool{"rc": true},
			string(model.SourceCLI):                map[string]bool{"cli": true},
			string(model.SourceProvided):           map[string]bool{"provided": true},
		})
		return string(data), err
	}
}

func getSecurityAgentComp(t *testing.T, enableConfig bool) *secagent {
	l := logmock.New(t)

	cfg := config.NewMock(t)
	cfg.Set("inventories_configuration_enabled", enableConfig, model.SourceUnknown)

	r := Requires{
		Log:        l,
		Config:     cfg,
		Serializer: serializermock.NewMetricSerializer(t),
		IPC: fxutil.Test[ipc.Component](t,
			fx.Provide(func() ipc.Component { return ipcmock.New(t) }),
			fx.Provide(func() log.Component { return l }),
			fx.Provide(func() config.Component { return cfg }),
		),
	}

	comp := NewComponent(r).Comp
	return comp.(*secagent)
}

func assertPayload(t *testing.T, p *Payload) {
	assert.Equal(t, "test hostname", p.Hostname)
	assert.True(t, p.Timestamp <= time.Now().UnixNano())
	assert.Equal(t,
		map[string]interface{}{
			"agent_runtime_configuration":        "runtime: true\n",
			"cli_configuration":                  "cli: true\n",
			"environment_variable_configuration": "env: true\n",
			"file_configuration":                 "file: true\n",
			"full_configuration":                 "full config",
			"provided_configuration":             "provided: true\n",
			"remote_configuration":               "rc: true\n",
			"source_local_configuration":         "local: true\n",
			"agent_version":                      version.AgentVersion,
		},
		p.Metadata)
}

func TestGetPayload(t *testing.T) {
	setupFetcher(t)
	sa := getSecurityAgentComp(t, true)

	sa.hostname = "test hostname"

	p := sa.getPayload().(*Payload)
	assertPayload(t, p)
}

func TestGetPayloadNoConfig(t *testing.T) {
	setupFetcher(t)
	sa := getSecurityAgentComp(t, false)

	sa.hostname = "test hostname"

	p := sa.getPayload().(*Payload)
	assert.Equal(t, "test hostname", p.Hostname)
	assert.True(t, p.Timestamp <= time.Now().UnixNano())
	assert.Equal(t,
		map[string]interface{}{
			"agent_version": version.AgentVersion,
		},
		p.Metadata)
}

func TestWritePayload(t *testing.T) {
	setupFetcher(t)
	sa := getSecurityAgentComp(t, true)

	sa.hostname = "test hostname"

	req := httptest.NewRequest("GET", "http://fake_url.com", nil)
	w := httptest.NewRecorder()

	sa.writePayloadAsJSON(w, req)

	resp := w.Result()
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.NoError(t, err)

	p := Payload{}
	err = json.Unmarshal(body, &p)
	require.NoError(t, err)

	assertPayload(t, &p)
}
