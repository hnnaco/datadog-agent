// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build python

package python

import (
	"errors"
	"expvar"
	"fmt"
	"strings"
	"sync"
	"unsafe"

	"github.com/mohae/deepcopy"

	"github.com/DataDog/datadog-agent/cmd/agent/common"
	"github.com/DataDog/datadog-agent/comp/core/autodiscovery/integration"
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	integrations "github.com/DataDog/datadog-agent/comp/logs/integrations/def"
	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/aggregator/sender"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	"github.com/DataDog/datadog-agent/pkg/collector/loaders"
	pkgconfigsetup "github.com/DataDog/datadog-agent/pkg/config/setup"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/option"
	"github.com/DataDog/datadog-agent/pkg/version"
)

/*
#include <stdlib.h>

#include "datadog_agent_rtloader.h"
#include "rtloader_mem.h"
*/
import "C"

var (
	pyLoaderStats    *expvar.Map
	configureErrors  map[string][]string
	py3Linted        map[string]struct{}
	py3Warnings      map[string][]string
	statsLock        sync.RWMutex
	py3LintedLock    sync.Mutex
	linterLock       sync.Mutex
	agentVersionTags []string
	pythonOnce       sync.Once
)

const (
	wheelNamespace = "datadog_checks"
	a7TagReady     = "ready"
	a7TagNotReady  = "not_ready"
	a7TagUnknown   = "unknown"
	a7TagPython3   = "python3" // Already running on python3, linting is disabled
)

// PythonCheckLoaderName is the name of the Python check loader
const PythonCheckLoaderName string = "python"

func init() {
	factory := func(senderManager sender.SenderManager, logReceiver option.Option[integrations.Component], tagger tagger.Component) (check.Loader, error) {
		return NewPythonCheckLoader(senderManager, logReceiver, tagger)
	}
	loaders.RegisterLoader(20, factory)

	configureErrors = map[string][]string{}
	py3Linted = map[string]struct{}{}
	py3Warnings = map[string][]string{}
	pyLoaderStats = expvar.NewMap("pyLoader")
	pyLoaderStats.Set("ConfigureErrors", expvar.Func(expvarConfigureErrors))
	pyLoaderStats.Set("Py3Warnings", expvar.Func(expvarPy3Warnings))

	agentVersionTags = []string{}
	if agentVersion, err := version.Agent(); err == nil {
		agentVersionTags = []string{
			fmt.Sprintf("agent_version_major:%d", agentVersion.Major),
			fmt.Sprintf("agent_version_minor:%d", agentVersion.Minor),
			fmt.Sprintf("agent_version_patch:%d", agentVersion.Patch),
		}
	}
}

// PythonCheckLoader is a specific loader for checks living in Python modules
//
//nolint:revive // TODO(AML) Fix revive linter
type PythonCheckLoader struct {
	logReceiver option.Option[integrations.Component]
}

// NewPythonCheckLoader creates an instance of the Python checks loader
func NewPythonCheckLoader(senderManager sender.SenderManager, logReceiver option.Option[integrations.Component], tagger tagger.Component) (*PythonCheckLoader, error) {
	initializeCheckContext(senderManager, logReceiver, tagger)
	return &PythonCheckLoader{
		logReceiver: logReceiver,
	}, nil
}

func getRtLoaderError() error {
	if C.has_error(rtloader) == 1 {
		cErr := C.get_error(rtloader)
		return errors.New(C.GoString(cErr))
	}
	return nil
}

// Name returns Python loader name
func (*PythonCheckLoader) Name() string {
	return PythonCheckLoaderName
}

// Load tries to import a Python module with the same name found in config.Name, searches for
// subclasses of the AgentCheck class and returns the corresponding Check
func (cl *PythonCheckLoader) Load(senderManager sender.SenderManager, config integration.Config, instance integration.Data) (check.Check, error) {
	if pkgconfigsetup.Datadog().GetBool("python_lazy_loading") {
		pythonOnce.Do(func() {
			InitPython(common.GetPythonPaths()...)
		})
	}

	if rtloader == nil {
		return nil, fmt.Errorf("python is not initialized")
	}
	moduleName := config.Name
	// FastDigest is used as check id calculation does not account for tags order
	configDigest := config.FastDigest()

	// Lock the GIL
	glock, err := newStickyLock()
	if err != nil {
		return nil, err
	}
	defer glock.unlock()

	// Platform-specific preparation
	if !pkgconfigsetup.Datadog().GetBool("win_skip_com_init") {
		log.Debugf("Performing platform loading prep")
		err = platformLoaderPrep()
		if err != nil {
			return nil, err
		}
		defer platformLoaderDone() //nolint:errcheck
	} else {
		log.Infof("Skipping platform loading prep")
	}

	// Looking for wheels first
	modules := []string{fmt.Sprintf("%s.%s", wheelNamespace, moduleName), moduleName}
	var loadedAsWheel bool

	var name string
	var checkModule *C.rtloader_pyobject_t
	var checkClass *C.rtloader_pyobject_t
	for _, name = range modules {
		// TrackedCStrings untracked by memory tracker currently
		moduleName := TrackedCString(name)
		defer C._free(unsafe.Pointer(moduleName))
		if res := C.get_class(rtloader, moduleName, &checkModule, &checkClass); res != 0 {
			if strings.HasPrefix(name, fmt.Sprintf("%s.", wheelNamespace)) {
				loadedAsWheel = true
			}
			break
		}

		if err = getRtLoaderError(); err != nil {
			log.Debugf("Unable to load python module - %s: %v", name, err)
		} else {
			log.Debugf("Unable to load python module - %s", name)
		}
	}

	// all failed, return error for last failure
	if checkModule == nil || checkClass == nil {
		log.Debugf("PyLoader returning %s for %s", err, moduleName)
		return nil, err
	}

	wheelVersion := "unversioned"
	// getting the wheel version for the check
	var version *C.char

	// TrackedCStrings untracked by memory tracker currently
	versionAttr := TrackedCString("__version__")
	defer C._free(unsafe.Pointer(versionAttr))
	// get_attr_string allocation tracked by memory tracker
	if res := C.get_attr_string(rtloader, checkModule, versionAttr, &version); res != 0 {
		wheelVersion = C.GoString(version)
		C.rtloader_free(rtloader, unsafe.Pointer(version))
	} else {
		log.Debugf("python check '%s' doesn't have a '__version__' attribute: %s", config.Name, getRtLoaderError())
	}

	if !pkgconfigsetup.Datadog().GetBool("disable_py3_validation") && !loadedAsWheel {
		// Customers, though unlikely might version their custom checks.
		// Let's use the module namespace to try to decide if this was a
		// custom check, check for py3 compatibility
		var checkFilePath *C.char
		var goCheckFilePath string

		fileAttr := TrackedCString("__file__")
		defer C._free(unsafe.Pointer(fileAttr))
		// get_attr_string allocation tracked by memory tracker
		if res := C.get_attr_string(rtloader, checkModule, fileAttr, &checkFilePath); res != 0 {
			goCheckFilePath = C.GoString(checkFilePath)
			C.rtloader_free(rtloader, unsafe.Pointer(checkFilePath))
		} else {
			log.Debugf("Could not query the __file__ attribute for check %s: %s", name, getRtLoaderError())
		}

		go reportPy3Warnings(name, goCheckFilePath)
	}

	var goHASupported bool
	if pkgconfigsetup.Datadog().GetBool("ha_agent.enabled") {
		var haSupported C.bool

		haSupportedAttr := TrackedCString("HA_SUPPORTED")
		defer C._free(unsafe.Pointer(haSupportedAttr))
		if res := C.get_attr_bool(rtloader, checkClass, haSupportedAttr, &haSupported); res != 0 {
			goHASupported = haSupported == C.bool(true)
		} else {
			log.Debugf("Could not query the HA_SUPPORTED attribute for check %s: %s", name, getRtLoaderError())
		}
	}

	c, err := NewPythonCheck(senderManager, moduleName, checkClass, goHASupported)
	if err != nil {
		return c, err
	}

	// The GIL should be unlocked at this point, `check.Configure` uses its own stickyLock and stickyLocks must not be nested
	if err := c.Configure(senderManager, configDigest, instance, config.InitConfig, config.Source); err != nil {
		C.rtloader_decref(rtloader, checkClass)
		C.rtloader_decref(rtloader, checkModule)

		if errors.Is(err, check.ErrSkipCheckInstance) {
			return nil, err
		}

		addExpvarConfigureError(fmt.Sprintf("%s (%s)", moduleName, wheelVersion), err.Error())
		return c, fmt.Errorf("could not configure check instance for python check %s: %s", moduleName, err.Error())
	}

	if v, ok := cl.logReceiver.Get(); ok {
		v.RegisterIntegration(string(c.id), config)
	}

	c.version = wheelVersion
	C.rtloader_decref(rtloader, checkClass)
	C.rtloader_decref(rtloader, checkModule)

	log.Debugf("python loader: done loading check %s (version %s)", moduleName, wheelVersion)
	return c, nil
}

func (cl *PythonCheckLoader) String() string {
	return "Python Check Loader"
}

func expvarConfigureErrors() interface{} {
	statsLock.RLock()
	defer statsLock.RUnlock()

	return deepcopy.Copy(configureErrors)
}

func addExpvarConfigureError(check string, errMsg string) {
	log.Errorf("py.loader: could not configure check '%s': %s", check, errMsg)

	statsLock.Lock()
	defer statsLock.Unlock()

	if errors, ok := configureErrors[check]; ok {
		configureErrors[check] = append(errors, errMsg)
	} else {
		configureErrors[check] = []string{errMsg}
	}
}

func expvarPy3Warnings() interface{} {
	statsLock.RLock()
	defer statsLock.RUnlock()

	return deepcopy.Copy(py3Warnings)
}

// reportPy3Warnings runs the a7 linter and exports the result in both expvar
// and the aggregator (as extra series)
func reportPy3Warnings(checkName string, checkFilePath string) {
	// check if the check has already been linted
	py3LintedLock.Lock()
	_, found := py3Linted[checkName]
	if found {
		py3LintedLock.Unlock()
		return
	}
	py3Linted[checkName] = struct{}{}
	py3LintedLock.Unlock()

	status := a7TagUnknown
	metricValue := 0.0
	if checkFilePath != "" {
		// __file__ return the .pyc file path
		if strings.HasSuffix(checkFilePath, ".pyc") {
			checkFilePath = checkFilePath[:len(checkFilePath)-1]
		}

		if strings.TrimSpace(pkgconfigsetup.Datadog().GetString("python_version")) == "3" {
			// the linter used by validatePython3 doesn't work when run from python3
			status = a7TagPython3
			metricValue = 1.0
		} else {
			// validatePython3 is CPU and memory hungry, make sure we only run one instance of it
			// at once to avoid CPU and mem usage spikes
			linterLock.Lock()
			warnings, err := validatePython3(checkName, checkFilePath)
			linterLock.Unlock()

			if err != nil {
				status = a7TagUnknown
				log.Warnf("Failed to validate Python 3 linting for check '%s': '%s'", checkName, err)
			} else if len(warnings) == 0 {
				status = a7TagReady
				metricValue = 1.0
			} else {
				status = a7TagNotReady
				log.Warnf("The Python 3 linter returned warnings for check '%s'. For more details, check the output of the 'status' command or the status page of the Agent GUI).", checkName)
				statsLock.Lock()
				defer statsLock.Unlock()
				for _, warning := range warnings {
					log.Debug(warning)
					py3Warnings[checkName] = append(py3Warnings[checkName], warning)
				}
			}
		}
	}

	// add a serie to the aggregator to be sent on every flush
	tags := []string{
		fmt.Sprintf("status:%s", status),
		fmt.Sprintf("check_name:%s", checkName),
	}
	tags = append(tags, agentVersionTags...)
	aggregator.AddRecurrentSeries(&metrics.Serie{
		Name:   "datadog.agent.check_ready",
		Points: []metrics.Point{{Value: metricValue}},
		Tags:   tagset.CompositeTagsFromSlice(tags),
		MType:  metrics.APIGaugeType,
	})
}
