// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build test

package server

import (
	"testing"
	"time"

	"github.com/DataDog/datadog-agent/pkg/config/mock"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	pkglogsetup "github.com/DataDog/datadog-agent/pkg/util/log/setup"

	"github.com/DataDog/datadog-agent/comp/dogstatsd/packets"
)

func buildPacketContent(numberOfMetrics int, nbValuePerMessage int) []byte {
	values := ""
	for i := 0; i < nbValuePerMessage; i++ {
		values += ":666"
	}
	rawPacket := "daemon" + values + "|h|@0.5|#sometag1:somevalue1,sometag2:somevalue2"
	packets := rawPacket
	for i := 1; i < numberOfMetrics; i++ {
		packets += "\n" + rawPacket
	}
	return []byte(packets)
}

func benchParsePackets(b *testing.B, rawPacket []byte) {
	cfg := mock.New(b)
	deps := fulfillDeps(b)
	s := deps.Server.(*server)
	// our logger will log dogstatsd packet by default if nothing is setup
	pkglogsetup.SetupLogger("", "off", "", "", false, true, false, cfg)

	histogram := deps.Telemetry.NewHistogram("test-dogstatsd",
		"channel_latency",
		[]string{"shard", "message_type"},
		"Time in nanosecond to push metrics to the aggregator input buffer",
		defaultChannelBuckets)

	demux := deps.Demultiplexer
	defer demux.Stop(false)

	done := make(chan struct{})
	go func() {
		s, l := demux.WaitForSamples(time.Millisecond * 1)
		if len(s) > 0 || len(l) > 0 {
			return
		}
	}()
	defer close(done)

	b.RunParallel(func(pb *testing.PB) {
		batcher := newBatcher(demux, histogram)
		parser := newParser(deps.Config, s.sharedFloat64List, 1, deps.WMeta, s.stringInternerTelemetry)
		packet := packets.Packet{
			Contents: rawPacket,
			Origin:   packets.NoOrigin,
		}

		packets := packets.Packets{&packet}
		samples := make([]metrics.MetricSample, 0, 512)
		for pb.Next() {
			packet.Contents = rawPacket
			samples = s.parsePackets(batcher, parser, packets, samples)
		}
	})
}

func BenchmarkParsePackets(b *testing.B) {
	// 640 packets of 1 samples
	benchParsePackets(b, buildPacketContent(20*32, 1))
}

func BenchmarkParsePacketsMultiple(b *testing.B) {
	// 64 packets of 10 samples
	benchParsePackets(b, buildPacketContent(2*32, 10))
}

var samplesBench []metrics.MetricSample

func BenchmarkPbarseMetricMessage(b *testing.B) {
	cfg := mock.New(b)
	deps := fulfillDeps(b)
	s := deps.Server.(*server)
	// our logger will log dogstatsd packet by default if nothing is setup
	pkglogsetup.SetupLogger("", "off", "", "", false, true, false, cfg)

	demux := deps.Demultiplexer

	done := make(chan struct{})
	go func() {
		s, l := demux.WaitForSamples(time.Millisecond * 1)
		if len(s) > 0 || len(l) > 0 {
			return
		}
	}()
	defer close(done)

	stringInternerTelemetry := newSiTelemetry(false, deps.Telemetry)
	parser := newParser(deps.Config, newFloat64ListPool(deps.Telemetry), 1, deps.WMeta, stringInternerTelemetry)
	message := []byte("daemon:666|h|@0.5|#sometag1:somevalue1,sometag2:somevalue2")

	b.RunParallel(func(pb *testing.PB) {
		samplesBench = make([]metrics.MetricSample, 0, 512)
		for pb.Next() {
			s.parseMetricMessage(samplesBench, parser, message, "", 0, "", false)
			samplesBench = samplesBench[0:0]
		}
	})
}

func BenchmarkWithMapper(b *testing.B) {
	datadogYaml := `
dogstatsd_mapper_profiles:
 - name: airflow
   prefix: 'airflow.'
   mappings:
     - match: "airflow.job.duration.*.*"       # metric format: airflow.job.duration.<job_type>.<job_name>
       name: "airflow.job.duration"            # remap the metric name
       tags:
         job_type: "$1"
         job_name: "$2"
     - match: "airflow.job.size.*.*"           # metric format: airflow.job.size.<job_type>.<job_name>
       name: "airflow.job.size"                # remap the metric name
       tags:
         foo: "$1"
         bar: "$2"
`

	benchmarkMapperControl(b, datadogYaml)
}

func benchmarkMapperControl(b *testing.B, yaml string) {
	deps := fulfillDepsWithConfigYaml(b, yaml)
	cfg := mock.New(b)
	s := deps.Server.(*server)

	// our logger will log dogstatsd packet by default if nothing is setup
	pkglogsetup.SetupLogger("", "off", "", "", false, true, false, cfg)

	demux := deps.Demultiplexer

	histogram := deps.Telemetry.NewHistogram("dogstatsd",
		"channel_latency",
		[]string{"shard", "message_type"},
		"Time in nanosecond to push metrics to the aggregator input buffer",
		defaultChannelBuckets)

	done := make(chan struct{})
	go func() {
		s, l := demux.WaitForSamples(time.Millisecond * 1)
		if len(s) > 0 || len(l) > 0 {
			return
		}
	}()
	defer close(done)

	batcher := newBatcher(demux, histogram)
	stringInternerTelemetry := newSiTelemetry(false, deps.Telemetry)
	parser := newParser(deps.Config, newFloat64ListPool(deps.Telemetry), 1, deps.WMeta, stringInternerTelemetry)

	samples := make([]metrics.MetricSample, 0, 512)
	for n := 0; n < b.N; n++ {
		packet := packets.Packet{
			Contents: []byte("airflow.job.duration.my_job_type.my_job_name:666|g"),
			Origin:   packets.NoOrigin,
		}
		packets := packets.Packets{&packet}
		samples = s.parsePackets(batcher, parser, packets, samples)
	}

	b.ReportAllocs()
}
