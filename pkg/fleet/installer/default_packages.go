// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package installer

import (
	"regexp"
	"slices"
	"strings"

	"github.com/DataDog/datadog-agent/pkg/fleet/installer/env"
	"github.com/DataDog/datadog-agent/pkg/fleet/installer/oci"
)

// Package represents a package known to the installer
type Package struct {
	Name                      string
	version                   func(Package, *env.Env) string
	released                  bool
	releasedBySite            []string
	releasedWithRemoteUpdates bool
	condition                 func(Package, *env.Env) bool
}

// PackagesList lists all known packages. Not all of them are installable
var PackagesList = []Package{
	{Name: "datadog-apm-inject", version: apmInjectVersion, released: true, condition: apmInjectEnabled},
	{Name: "datadog-apm-library-java", version: apmJavaVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-apm-library-ruby", version: apmLanguageVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-apm-library-js", version: apmLanguageVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-apm-library-dotnet", version: apmLanguageVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-apm-library-python", version: apmLanguageVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-apm-library-php", version: apmLanguageVersion, released: true, condition: apmLanguageEnabled},
	{Name: "datadog-agent", version: agentVersion, released: false, releasedWithRemoteUpdates: true},
}

// Default versions pinned for CentOS 6
// This is in place to make sure we don't break backward compatibility for the few
// customers using SSI that are still using CentOS 6
// No manual testing is done on CentOS 6, so we can't guarantee that the latest versions will continue working.
// Before updating the pin, please make sure the pinned packages still work.
var (
	apmInjectCentos6Version = "0.30.0-1"
	apmJavaCentos6Version   = "1.45.2-1"
)

var apmPackageDefaultVersions = map[string]string{
	"datadog-apm-library-java":   "1",
	"datadog-apm-library-ruby":   "2",
	"datadog-apm-library-js":     "5",
	"datadog-apm-library-dotnet": "3",
	"datadog-apm-library-python": "2",
	"datadog-apm-library-php":    "1",
}

// DefaultPackages resolves the default packages URLs to install based on the environment.
func DefaultPackages(env *env.Env) []string {
	return defaultPackages(env, PackagesList)
}

func defaultPackages(env *env.Env, defaultPackages []Package) []string {
	var packages []string
	for _, p := range defaultPackages {
		released := p.released || slices.Contains(p.releasedBySite, env.Site) || (p.releasedWithRemoteUpdates && env.RemoteUpdates)
		installOverride, isOverridden := env.DefaultPackagesInstallOverride[p.Name]
		condition := p.condition == nil || p.condition(p, env)

		shouldInstall := released && condition
		if isOverridden {
			shouldInstall = installOverride
		}
		if !shouldInstall {
			continue
		}

		version := "latest"
		if p.version != nil {
			version = p.version(p, env)
		}
		if v, ok := env.DefaultPackagesVersionOverride[p.Name]; ok {
			version = v
		}
		url := oci.PackageURL(env, p.Name, version)
		packages = append(packages, url)
	}
	return packages
}

func apmInjectEnabled(_ Package, e *env.Env) bool {
	switch e.InstallScript.APMInstrumentationEnabled {
	case env.APMInstrumentationEnabledAll, env.APMInstrumentationEnabledDocker, env.APMInstrumentationEnabledHost:
		return true
	}
	return false
}

// apmLanguageEnabled returns true if the package should be installed
func apmLanguageEnabled(p Package, e *env.Env) bool {
	if _, ok := e.ApmLibraries[packageToLanguage(p.Name)]; ok {
		return true
	}
	if _, ok := e.ApmLibraries["all"]; ok {
		return true
	}
	// If the ApmLibraries env is left empty but apm injection is
	// enabled, we install all languages
	if len(e.ApmLibraries) == 0 && apmInjectEnabled(p, e) {
		return true
	}
	return false
}

var fullSemverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+`)

func apmLanguageVersion(p Package, e *env.Env) string {
	version := "latest"
	if defaultVersion, ok := apmPackageDefaultVersions[p.Name]; ok {
		version = defaultVersion
	}

	apmLibVersion := e.ApmLibraries[packageToLanguage(p.Name)]
	if apmLibVersion == "" {
		return version
	}

	versionTag, _ := strings.CutPrefix(string(apmLibVersion), "v")
	if fullSemverRe.MatchString(versionTag) {
		return versionTag + "-1"
	}
	return versionTag
}

func apmJavaVersion(p Package, e *env.Env) string {
	if e.IsCentos6 {
		apmLibVersion := e.ApmLibraries[packageToLanguage(p.Name)]
		// If no version is set, or the customer specifies major version 1, return the pinned version
		if apmLibVersion == "" || apmLibVersion == "1" {
			return apmJavaCentos6Version
		}
	}
	return apmLanguageVersion(p, e)
}

// apmInjectVersion returns the version of the apm-inject package to install
// If centos6 is detected return the pin, otherwise alwasys return latest
func apmInjectVersion(_p Package, e *env.Env) string {
	version := "latest"
	if e.IsCentos6 {
		return apmInjectCentos6Version
	}
	return version
}

func packageToLanguage(packageName string) env.ApmLibLanguage {
	lang, found := strings.CutPrefix(packageName, "datadog-apm-library-")
	if !found {
		return ""
	}
	return env.ApmLibLanguage(lang)
}

func agentVersion(_ Package, e *env.Env) string {
	return e.GetAgentVersion()
}
