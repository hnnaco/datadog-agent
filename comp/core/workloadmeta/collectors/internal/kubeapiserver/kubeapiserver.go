// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver

// Package kubeapiserver implements the kubeapiserver Workloadmeta collector.
package kubeapiserver

import (
	"context"
	"strings"
	"time"

	"go.uber.org/fx"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/DataDog/datadog-agent/comp/core/config"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	"github.com/DataDog/datadog-agent/pkg/config/structure"
	configutils "github.com/DataDog/datadog-agent/pkg/config/utils"
	"github.com/DataDog/datadog-agent/pkg/status/health"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/apiserver"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	collectorID   = "kubeapiserver"
	componentName = "workloadmeta-kubeapiserver"
	noResync      = time.Duration(0)
)

type dependencies struct {
	fx.In

	Config config.Component
}

// storeGenerator returns a new store specific to a given resource
type storeGenerator func(context.Context, workloadmeta.Component, config.Reader, kubernetes.Interface) (*cache.Reflector, *reflectorStore)

func shouldHavePodStore(cfg config.Reader) bool {
	metadataAsTags := configutils.GetMetadataAsTags(cfg)
	hasPodLabelsAsTags := len(metadataAsTags.GetPodLabelsAsTags()) > 0
	hasPodAnnotationsAsTags := len(metadataAsTags.GetPodAnnotationsAsTags()) > 0

	return cfg.GetBool("cluster_agent.collect_kubernetes_tags") || cfg.GetBool("autoscaling.workload.enabled") || hasPodLabelsAsTags || hasPodAnnotationsAsTags
}

func shouldHaveDeploymentStore(cfg config.Reader) bool {
	metadataAsTags := configutils.GetMetadataAsTags(cfg)
	hasDeploymentsLabelsAsTags := len(metadataAsTags.GetResourcesLabelsAsTags()["deployments.apps"]) > 0
	hasDeploymentsAnnotationsAsTags := len(metadataAsTags.GetResourcesAnnotationsAsTags()["deployments.apps"]) > 0

	return cfg.GetBool("language_detection.enabled") && cfg.GetBool("language_detection.reporting.enabled") || hasDeploymentsLabelsAsTags || hasDeploymentsAnnotationsAsTags
}

func storeGenerators(cfg config.Reader) []storeGenerator {
	var generators []storeGenerator

	if shouldHavePodStore(cfg) {
		generators = append(generators, newPodStore)
	}

	if shouldHaveDeploymentStore(cfg) {
		generators = append(generators, newDeploymentStore)
	}

	return generators
}

func metadataCollectionGVRs(cfg config.Reader, discoveryClient discovery.DiscoveryInterface) ([]schema.GroupVersionResource, error) {
	return getGVRsForRequestedResources(discoveryClient, resourcesWithMetadataCollectionEnabled(cfg))
}

func resourcesWithMetadataCollectionEnabled(cfg config.Reader) []string {
	resources := append(
		resourcesWithRequiredMetadataCollection(cfg),
		resourcesWithExplicitMetadataCollectionEnabled(cfg)...,
	)

	// Remove duplicates and return
	return cleanDuplicateVersions(resources)
}

// resourcesWithRequiredMetadataCollection returns the list of resources that we
// need to collect metadata from in order to make other enabled features work
func resourcesWithRequiredMetadataCollection(cfg config.Reader) []string {
	res := []string{"nodes"} // nodes are always needed

	metadataAsTags := configutils.GetMetadataAsTags(cfg)

	for groupResource, labelsAsTags := range metadataAsTags.GetResourcesLabelsAsTags() {

		if strings.HasPrefix(groupResource, "pods") || strings.HasPrefix(groupResource, "deployments") || len(labelsAsTags) == 0 {
			continue
		}
		requestedResource := groupResourceToGVRString(groupResource)
		if requestedResource != "" {
			res = append(res, requestedResource)
		}
	}

	for groupResource, annotationsAsTags := range metadataAsTags.GetResourcesAnnotationsAsTags() {
		if strings.HasPrefix(groupResource, "pods") || strings.HasPrefix(groupResource, "deployments") || len(annotationsAsTags) == 0 {
			continue
		}
		requestedResource := groupResourceToGVRString(groupResource)
		if requestedResource != "" {
			res = append(res, requestedResource)
		}
	}

	for _, groupResource := range resourcesForAPMConfig(cfg) {
		requestedResource := groupResourceToGVRString(groupResource)
		if requestedResource != "" {
			res = append(res, requestedResource)
		}
	}

	return res
}

// resourcesWithExplicitMetadataCollectionEnabled returns the list of resources
// to collect metadata from according to the config options that configure
// metadata collection
// Pods and/or Deployments are excluded if they have their separate stores and informers
// in order to avoid having two collectors collecting the same data.
func resourcesWithExplicitMetadataCollectionEnabled(cfg config.Reader) []string {
	if !cfg.GetBool("cluster_agent.kube_metadata_collection.enabled") {
		return nil
	}

	var resources []string
	requestedResources := cfg.GetStringSlice("cluster_agent.kube_metadata_collection.resources")
	for _, resource := range requestedResources {
		if strings.HasSuffix(resource, "pods") {
			log.Debugf("skipping pods from metadata collection because a separate pod store is initialised in workload metadata store.")
			continue
		}

		if strings.HasSuffix(resource, "deployments") {
			log.Debugf("skipping deployments from metadata collection because a separate deployment store is initialised in workload metadata store.")
			continue
		}

		resources = append(resources, resource)
	}

	return resources
}

// resourcesForAPMConfig returns the list of resources to collect metadata from
// for the auto instrumentation configuration. Namespaces are collected in order
// to utilize namespace labels for target based configuration.
func resourcesForAPMConfig(cfg config.Reader) []string {
	// If APM is not enabled, we don't need to collect any resources for the
	// auto instrumentation configuration.
	apmEnabled := cfg.GetBool("apm_config.instrumentation.enabled")
	if !apmEnabled {
		return nil
	}

	// Targets is a custom struct type, so we unmarshal it into an interface
	// slice to avoid the import while still being able to check if it's empty.
	// For simplicity, we enable the namespace collection if there are any
	// targets defined and do not check if the targets actually require
	// namespace labels.
	targets := []interface{}{}
	err := structure.UnmarshalKey(cfg, "apm_config.instrumentation.targets", &targets)
	if err != nil {
		log.Errorf("failed to unmarshal apm_config.instrumentation.targets: %v", err)
		return nil
	}

	// If there are no targets, we don't need to collect any resources.
	if len(targets) == 0 {
		return nil
	}

	return []string{"namespaces"}
}

type collector struct {
	id      string
	catalog workloadmeta.AgentType
	config  config.Reader
}

// NewCollector returns a kubeapiserver CollectorProvider that instantiates its colletor
func NewCollector(deps dependencies) (workloadmeta.CollectorProvider, error) {
	return workloadmeta.CollectorProvider{
		Collector: &collector{
			id:      collectorID,
			catalog: workloadmeta.ClusterAgent,
			config:  deps.Config,
		},
	}, nil
}

// GetFxOptions returns the FX framework options for the collector
func GetFxOptions() fx.Option {
	return fx.Provide(NewCollector)
}

func (c *collector) Start(ctx context.Context, wlmetaStore workloadmeta.Component) error {
	var objectStores []*reflectorStore

	apiserverClient, err := apiserver.GetAPIClient()
	if err != nil {
		return err
	}
	client := apiserverClient.InformerCl

	metadataclient, err := apiserverClient.MetadataClient()
	if err != nil {
		return err
	}

	// Initialize metadata collection informers
	gvrs, err := metadataCollectionGVRs(c.config, client.Discovery())

	if err != nil {
		log.Errorf("failed to discover Group and Version of requested resources: %v", err)
	} else {
		for _, gvr := range gvrs {
			reflector, store := newMetadataStore(ctx, wlmetaStore, c.config, metadataclient, gvr)
			objectStores = append(objectStores, store)
			go reflector.Run(ctx.Done())
		}
	}

	for _, storeBuilder := range storeGenerators(c.config) {
		reflector, store := storeBuilder(ctx, wlmetaStore, c.config, client)
		objectStores = append(objectStores, store)
		go reflector.Run(ctx.Done())
	}

	go runStartupCheck(ctx, objectStores)

	return nil
}

func (c *collector) Pull(_ context.Context) error {
	return nil
}

func (c *collector) GetID() string {
	return c.id
}

func (c *collector) GetTargetCatalog() workloadmeta.AgentType {
	return c.catalog
}

func runStartupCheck(ctx context.Context, stores []*reflectorStore) {
	log.Infof("Starting startup health check waiting for %d k8s reflectors to sync", len(stores))

	// There is no way to ensure liveness correctly as it would need to be plugged inside the
	// inner loop of Reflector.
	// However, we add Startup when we got at least some data.
	startupHealthCheck := health.RegisterReadiness(componentName, health.Once)

	// Checked synced, in its own scope to cleanly un-reference the syncTimer
	{
		syncTimer := time.NewTicker(time.Second)
	OUTER:
		for {
			select {
			case <-ctx.Done():
				break OUTER

			case <-syncTimer.C:
				allSynced := true
				for _, store := range stores {
					allSynced = allSynced && store.HasSynced()
				}

				if allSynced {
					break OUTER
				}
			}
		}
		syncTimer.Stop()
	}

	// Once synced, validate startup health check
	log.Infof("All (%d) K8S reflectors synced to workloadmeta", len(stores))
	<-startupHealthCheck.C
}
