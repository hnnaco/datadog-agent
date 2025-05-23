// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build clusterchecks

package clusterchecks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	taggerfxmock "github.com/DataDog/datadog-agent/comp/core/tagger/fx-mock"
	"github.com/DataDog/datadog-agent/pkg/clusteragent/api"
	"github.com/DataDog/datadog-agent/pkg/clusteragent/clusterchecks/types"
	"github.com/DataDog/datadog-agent/pkg/util/testutil"
)

var (
	waitfor = 2 * time.Second
	tick    = 10 * time.Millisecond
)

func (h *Handler) assertLeadershipMessage(t *testing.T, expected state) {
	t.Helper()
	select {
	case value := <-h.leadershipChan:
		assert.Equal(t, expected, value)
	case <-time.After(50 * time.Millisecond):
		assert.Fail(t, "Timeout while waiting for channel message")
	}
}

func (h *Handler) assertNoLeadershipMessage(t *testing.T) {
	t.Helper()
	select {
	case <-h.leadershipChan:
		assert.Fail(t, "Unexpected channel message")
	case <-time.After(50 * time.Millisecond):
		return
	}
}

func TestUpdateLeaderIP(t *testing.T) {
	le := &fakeLeaderEngine{}
	h := &Handler{
		leadershipChan:       make(chan state, 1),
		leaderStatusCallback: le.get,
	}

	// First run, become leader
	le.set("", nil)
	err := h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "", h.leaderIP)
	h.assertLeadershipMessage(t, leader)

	// Second run, still leader, no update
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "", h.leaderIP)
	h.assertNoLeadershipMessage(t)

	// Query error
	queryError := errors.New("test query error")
	le.set("1.2.3.4", queryError)
	err = h.updateLeaderIP()
	assert.Equal(t, queryError, err)
	assert.Equal(t, "", h.leaderIP)
	h.assertNoLeadershipMessage(t)

	// Lose leadership
	le.set("1.2.3.4", nil)
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", h.leaderIP)
	h.assertLeadershipMessage(t, follower)

	// New leader, still following
	le.set("1.2.3.40", nil)
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.40", h.leaderIP)
	h.assertNoLeadershipMessage(t)

	// Back to leader
	le.set("", nil)
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "", h.leaderIP)
	h.assertLeadershipMessage(t, leader)

	// Start fresh, test unknown -> follower
	le = &fakeLeaderEngine{}
	h = &Handler{
		leadershipChan:       make(chan state, 1),
		leaderStatusCallback: le.get,

		leadershipStateNotifChan: le.subscribe(),
	}
	le.set("1.2.3.4", nil)
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "1.2.3.4", h.leaderIP)
	h.assertLeadershipMessage(t, follower)

	// Start fresh, test unknown -> unknown -> leader
	le = &fakeLeaderEngine{}
	h = &Handler{
		leadershipChan:       make(chan state, 1),
		leaderStatusCallback: le.get,

		leadershipStateNotifChan: le.subscribe(),
	}
	le.set("", errors.New("failing"))
	for i := 0; i < 4; i++ {
		err = h.updateLeaderIP()
		assert.Error(t, err)
		assert.Equal(t, "", h.leaderIP)
		assert.Equal(t, unknown, h.state)
		h.assertNoLeadershipMessage(t)
	}
	le.set("", nil)
	err = h.updateLeaderIP()
	assert.NoError(t, err)
	assert.Equal(t, "", h.leaderIP)
	assert.Equal(t, leader, h.state)
	h.assertLeadershipMessage(t, leader)
}

// TestHandlerRun tests the full lifecycle of the handling/dispatching
// lifecycle: unknown -> follower -> leader -> follower -> leader -> stop
func TestHandlerRun(t *testing.T) {
	dummyT := &testing.T{}
	ac := &mockedPluggableAutoConfig{}
	fakeTagger := taggerfxmock.SetupFakeTagger(t)
	ac.Test(t)
	le := newFakeLeaderEngine()

	testServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "I'm a teapot", 418)
	}))
	testPort := testServer.Listener.Addr().(*net.TCPAddr).Port
	defer testServer.Close()

	h := &Handler{
		autoconfig:           ac,
		warmupDuration:       250 * time.Millisecond,
		leadershipChan:       make(chan state, 1),
		dispatcher:           newDispatcher(fakeTagger),
		leaderStatusCallback: le.get,
		leaderForwarder:      api.NewLeaderForwarder(testPort, 10),

		leadershipStateNotifChan: le.subscribe(),
	}

	//
	// Initialisation and unknown state
	//
	ctx, cancelRun := context.WithCancel(context.Background())
	runReturned := make(chan struct{}, 1)
	go func() {
		h.Run(ctx)
		runReturned <- struct{}{}
	}()
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// State is unknown
		h.m.RLock()
		defer h.m.RUnlock()
		return h.state == unknown
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// API replys not ready, does not forward
		recorder := httptest.NewRecorder()
		res := h.RejectOrForwardLeaderQuery(recorder, httptest.NewRequest("GET", "https://dd-cluster-agent-service:5005/test", nil))
		if !res {
			return false
		}

		resp := recorder.Result()
		assert.NotNil(t, resp)
		assert.Equal(t, "503 Service Unavailable", resp.Status)
		assert.Equal(t, 503, resp.StatusCode)
		resp.Body.Close()
		return true
	})

	//
	// Unknown -> follower
	//
	le.set("127.0.0.1", nil)
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Internal state change
		h.m.RLock()
		defer h.m.RUnlock()
		return h.state == follower && h.leaderIP == "127.0.0.1"
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// API redirects to leader
		recorder := httptest.NewRecorder()
		res := h.RejectOrForwardLeaderQuery(recorder, httptest.NewRequest("GET", "https://dd-cluster-agent-service:5005/test", nil))
		if !res {
			return false
		}

		resp := recorder.Result()
		assert.NotNil(t, resp)
		assert.Equal(t, "418 I'm a teapot", resp.Status)
		assert.Equal(t, 418, resp.StatusCode)
		resp.Body.Close()
		return true
	})

	//
	// Follower -> leader
	//
	le.set("", nil)
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Internal state change
		h.m.RLock()
		defer h.m.RUnlock()
		return h.state == leader && h.leaderIP == ""
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		return !h.RejectOrForwardLeaderQuery(httptest.NewRecorder(), httptest.NewRequest("GET", "https://dd-cluster-agent-service:5005/test", nil))
	})
	ac.On("AddScheduler", schedulerName, mock.AnythingOfType("*clusterchecks.dispatcher"), true).Return()
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Keep node-agent caches even when timestamp is off (warmup)
		response := h.PostStatus("dummy", "10.0.0.1", types.NodeStatus{LastChange: -50})
		return response.IsUpToDate == true
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Test whether we're connected to the AD
		return ac.AssertNumberOfCalls(dummyT, "AddScheduler", 1)
	})

	// Schedule a check and make sure it is assigned to the node "dummy"
	testConfig := integration.Config{
		Name:         "unit_test",
		ClusterCheck: true,
	}
	h.dispatcher.Schedule([]integration.Config{testConfig})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Found one configuration for node dummy
		configs, err := h.GetConfigs("dummy")
		return err == nil && len(configs.Configs) == 1
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Flush node-agent caches when timestamp is off
		response := h.PostStatus("dummy", "10.0.0.1", types.NodeStatus{LastChange: -50})
		return response.IsUpToDate == false
	})

	//
	// Leader -> follower
	//
	ac.On("RemoveScheduler", schedulerName).Return()
	le.set("127.0.0.1", nil)
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Internal state change
		h.m.RLock()
		defer h.m.RUnlock()
		return h.state == follower && h.leaderIP == "127.0.0.1"
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// API redirects to leader
		recorder := httptest.NewRecorder()
		res := h.RejectOrForwardLeaderQuery(recorder, httptest.NewRequest("GET", "https://dd-cluster-agent-service:5005/test", nil))
		if !res {
			return false
		}

		resp := recorder.Result()
		assert.NotNil(t, resp)
		assert.Equal(t, "418 I'm a teapot", resp.Status)
		assert.Equal(t, 418, resp.StatusCode)
		resp.Body.Close()
		return true
	})

	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// RemoveScheduler is called
		return ac.AssertNumberOfCalls(dummyT, "RemoveScheduler", 1)
	})

	//
	// Follower -> leader again
	//
	le.set("", nil)
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// API redirects to leader
		return !h.RejectOrForwardLeaderQuery(httptest.NewRecorder(), httptest.NewRequest("GET", "https://dd-cluster-agent-service:5005/test", nil))
	})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Dispatcher has been flushed, no config remain
		state, err := h.GetState()
		return err == nil && len(state.Nodes) == 0 && len(state.Dangling) == 0
	})

	h.PostStatus("dummy", "10.0.0.1", types.NodeStatus{})
	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// Test whether we're connected to the AD
		return ac.AssertNumberOfCalls(dummyT, "AddScheduler", 2)
	})

	//
	// Leader -> stop
	//
	ac.On("RemoveScheduler", schedulerName).Return()
	cancelRun()
	select {
	case <-runReturned:
		// All good
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "Timeout while waiting for Run method to end")
	}

	testutil.AssertTrueBeforeTimeout(t, tick, waitfor, func() bool {
		// RemoveScheduler is called
		return ac.AssertNumberOfCalls(dummyT, "RemoveScheduler", 2)
	})
}
