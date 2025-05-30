// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf && !ebpf_bindata && !btfhubsync && !cws_go_generate

// Package ebpf holds ebpf related files
package ebpf

import (
	"github.com/DataDog/datadog-agent/pkg/ebpf/bytecode"
	"github.com/DataDog/datadog-agent/pkg/ebpf/bytecode/runtime"
	"github.com/DataDog/datadog-agent/pkg/security/probe/config"
)

// TODO change probe.c path to runtime-compilation specific version
//go:generate $GOPATH/bin/include_headers pkg/security/ebpf/c/prebuilt/probe.c pkg/ebpf/bytecode/build/runtime/runtime-security.c pkg/security/ebpf/c/include pkg/ebpf/c
//go:generate $GOPATH/bin/integrity pkg/ebpf/bytecode/build/runtime/runtime-security.c pkg/ebpf/bytecode/runtime/runtime-security.go runtime

func getRuntimeCompiledPrograms(config *config.Config, useSyscallWrapper, useFentry, useRingBuffer bool) (bytecode.AssetReader, error) {
	var cflags []string

	if useFentry {
		cflags = append(cflags, "-DUSE_FENTRY=1")
	}

	if useSyscallWrapper {
		cflags = append(cflags, "-DUSE_SYSCALL_WRAPPER=1")
	} else {
		cflags = append(cflags, "-DUSE_SYSCALL_WRAPPER=0")
	}

	if !config.NetworkEnabled {
		cflags = append(cflags, "-DDO_NOT_USE_TC")
	}

	if useRingBuffer {
		cflags = append(cflags, "-DUSE_RING_BUFFER=1")
	} else {
		cflags = append(cflags, "-DUSE_RING_BUFFER=0")
	}

	cflags = append(cflags, "-g")

	return runtime.RuntimeSecurity.Compile(&config.Config, cflags)
}
