// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build ignore

package redis

/*
#include "../../ebpf/c/protocols/redis/types.h"
#include "../../ebpf/c/protocols/classification/defs.h"
*/
import "C"

type ConnTuple = C.conn_tuple_t

type commandType = C.redis_command_t

var (
	getCommand = commandType(C.REDIS_GET)
	setCommand = commandType(C.REDIS_SET)
)

type EbpfEvent C.redis_event_t
type EbpfTx C.redis_transaction_t
