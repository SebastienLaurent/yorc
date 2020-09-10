// Copyright 2018 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hostspool

import (
	"context"
	"encoding/json"
	"github.com/ystia/yorc/v4/helper/collections"
	"strconv"
	"strings"
	"sync"

	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-multierror"
	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"

	"github.com/ystia/yorc/v4/config"
	"github.com/ystia/yorc/v4/deployments"
	"github.com/ystia/yorc/v4/events"
	"github.com/ystia/yorc/v4/helper/labelsutil"
	"github.com/ystia/yorc/v4/tasks"
	"github.com/ystia/yorc/v4/tosca"
	"github.com/ystia/yorc/v4/tosca/types"
)

const infrastructureType = "hostspool"

type defaultExecutor struct {
}

type operationParameters struct {
	location          string
	taskID            string
	deploymentID      string
	nodeName          string
	delegateOperation string
	hpManager         Manager
}

// Mutex to ensure consistency of a host allocations and resources labels
var hostsPoolAllocMutex sync.Mutex

func (e *defaultExecutor) ExecDelegate(ctx context.Context, cfg config.Configuration, taskID, deploymentID, nodeName, delegateOperation string) error {
	cc, err := cfg.GetConsulClient()
	if err != nil {
		return err
	}

	locationName, err := e.getLocationForNode(ctx, cfg, cc, deploymentID, nodeName)
	if err != nil {
		return err
	}

	operationParams := operationParameters{
		location:          locationName,
		taskID:            taskID,
		deploymentID:      deploymentID,
		nodeName:          nodeName,
		delegateOperation: delegateOperation,
		hpManager:         NewManager(cc, cfg),
	}
	return e.execDelegateHostsPool(ctx, cc, cfg, operationParams)
}

func (e *defaultExecutor) getLocationForNode(ctx context.Context, cfg config.Configuration, cc *api.Client, deploymentID, nodeName string) (string, error) {

	// Get current locations
	hpManager := NewManager(cc, cfg)
	locations, err := hpManager.ListLocations()
	if err != nil {
		return "", err
	}
	if locations == nil || len(locations) < 1 {
		return "", errors.Errorf("No location of type %q found", infrastructureType)
	}

	// Get the location name in node template metadata
	found, locationName, err := deployments.GetNodeMetadata(ctx, deploymentID, nodeName, tosca.MetadataLocationNameKey)
	if err != nil {
		return "", err
	}
	if !found {
		return locations[0], nil
	}

	if !collections.ContainsString(locations, locationName) {
		return "", errors.Errorf("No such location %q", locationName)
	}
	return locationName, nil
}

func (e *defaultExecutor) execDelegateHostsPool(
	ctx context.Context, cc *api.Client, cfg config.Configuration,
	op operationParameters) error {

	instances, err := tasks.GetInstances(ctx, op.taskID, op.deploymentID, op.nodeName)
	if err != nil {
		return err
	}
	allocatedResources, err := e.getAllocatedResourcesFromHostCapabilities(ctx, op.deploymentID, op.nodeName)
	if err != nil {
		return errors.Wrapf(err, "failed to retrieve allocated resources from host capabilities for node %q and deploymentID %q",
			op.nodeName, op.deploymentID)
	}

	switch strings.ToLower(op.delegateOperation) {
	case "install":
		setInstancesStateWithContextualLogs(ctx, op, instances, tosca.NodeStateCreating)

		genericResources, err := e.getGenericResourcesFromHostCapabilities(ctx, op.deploymentID, op.nodeName)
		if err != nil {
			return errors.Wrapf(err, "failed to retrieve allocated resources from host capabilities for node %q and deploymentID %q",
				op.nodeName, op.deploymentID)
		}
		err = e.hostsPoolCreate(ctx, cc, cfg, op, allocatedResources, genericResources)
		if err != nil {
			return err
		}
		setInstancesStateWithContextualLogs(ctx, op, instances, tosca.NodeStateStarted)
	case "uninstall":
		setInstancesStateWithContextualLogs(ctx, op, instances, tosca.NodeStateDeleting)
		err = e.hostsPoolDelete(ctx, cc, cfg, op, allocatedResources)
		if err != nil {
			return err
		}
		setInstancesStateWithContextualLogs(ctx, op, instances, tosca.NodeStateDeleted)
	default:
		return errors.Errorf("operation %q not supported", op.delegateOperation)
	}
	return nil
}

func (e *defaultExecutor) hostsPoolCreate(ctx context.Context,
	cc *api.Client, cfg config.Configuration,
	op operationParameters, allocatedResources map[string]string, genericResources []*GenericResource) error {

	jsonProp, err := deployments.GetNodePropertyValue(ctx, op.deploymentID, op.nodeName, "filters")
	if err != nil {
		return err
	}
	var filtersString []string
	if jsonProp != nil && jsonProp.RawString() != "" {
		err = json.Unmarshal([]byte(jsonProp.RawString()), &filtersString)
		if err != nil {
			return errors.Wrapf(err, `failed to parse property "filter" for node %q as json %q`, op.nodeName, jsonProp.String())
		}
	}
	filters, err := createFiltersFromComputeCapabilities(ctx, op.deploymentID, op.nodeName)
	if err != nil {
		return err
	}
	for i := range filtersString {
		f, err := labelsutil.CreateFilter(filtersString[i])
		if err != nil {
			return err
		}
		filters = append(filters, f)
	}
	shareable := false
	if s, err := deployments.GetNodePropertyValue(ctx, op.deploymentID, op.nodeName, "shareable"); err != nil {
		return err
	} else if s != nil && s.RawString() != "" {
		shareable, err = strconv.ParseBool(s.RawString())
		if err != nil {
			return err
		}
	}

	placement, err := e.getPlacementPolicy(ctx, op, op.nodeName)
	if err != nil {
		return err
	}

	instances, err := tasks.GetInstances(ctx, op.taskID, op.deploymentID, op.nodeName)
	if err != nil {
		return err
	}

	return e.allocateHostsToInstances(ctx, instances, shareable, filters, op, allocatedResources, placement, genericResources)
}

func (e *defaultExecutor) getPlacementPolicy(ctx context.Context, op operationParameters, target string) (string, error) {
	placementPolicies, err := deployments.GetPoliciesForTypeAndNode(ctx, op.deploymentID, placementPolicy, target)
	if err != nil {
		return "", err
	}

	if len(placementPolicies) == 0 {
		return "", nil
	}

	if len(placementPolicies) > 1 {
		return "", errors.Errorf("Found more than one placement policy to apply to node name:%q", target)
	}

	policyType, err := deployments.GetPolicyType(ctx, op.deploymentID, placementPolicies[0])
	if err != nil {
		return "", err
	}

	if err = op.hpManager.CheckPlacementPolicy(policyType); err != nil {
		return "", err
	}

	return policyType, nil
}

func (e *defaultExecutor) allocateHostsToInstances(
	originalCtx context.Context,
	instances []string,
	shareable bool,
	filters []labelsutil.Filter,
	op operationParameters,
	allocatedResources map[string]string,
	placement string,
	genericResources []*GenericResource) error {

	for _, instance := range instances {
		var instanceFilters []labelsutil.Filter
		ctx := events.AddLogOptionalFields(originalCtx, events.LogOptionalFields{events.InstanceID: instance})

		// Build generic resources filters by instance
		genericResourcesFilters, err := createGenericResourcesFilters(ctx, instance, genericResources)
		if err != nil {
			return err
		}
		instanceFilters = append(filters, genericResourcesFilters...)

		allocation := &Allocation{
			NodeName:         op.nodeName,
			Instance:         instance,
			DeploymentID:     op.deploymentID,
			Shareable:        shareable,
			Resources:        allocatedResources,
			PlacementPolicy:  placement,
			GenericResources: genericResources,
		}

		// Protecting the allocation and update of resources labels by a mutex, to
		// ensure no other worker will attempt to over-allocate resources of a
		// host if another worker has allocated but not yet updated resources labels
		hostsPoolAllocMutex.Lock()
		hostname, warnings, err := op.hpManager.Allocate(op.location, allocation, instanceFilters...)
		if err == nil {
			err = op.hpManager.UpdateResourcesLabels(op.location, hostname, allocatedResources, subtract, updateResourcesLabels, genericResources, removeElements, updateGenericResourcesLabels)
			if err != nil {
				return err
			}
		}
		hostsPoolAllocMutex.Unlock()

		for _, warn := range warnings {
			events.WithContextOptionalFields(ctx).
				NewLogEntry(events.LogLevelWARN, op.deploymentID).Registerf(`%v`, warn)
		}
		if err != nil {
			return err
		}

		if err = e.updateInstanceAttributes(ctx, op, hostname, instance, allocation.GenericResources); err != nil {
			return err
		}

		if err = e.updateConnectionSettings(ctx, op, hostname, instance); err != nil {
			return err
		}
	}
	return nil
}

func (e *defaultExecutor) updateInstanceAttributes(ctx context.Context, op operationParameters, hostname, instance string, gResources []*GenericResource) error {
	err := deployments.SetInstanceAttribute(ctx, op.deploymentID, op.nodeName, instance, "hostname", hostname)
	if err != nil {
		return err
	}

	// Create attributes for generic resources
	for _, gResource := range gResources {
		err = deployments.SetInstanceAttribute(ctx, op.deploymentID, op.nodeName, instance, gResource.Name, gResource.Value)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *defaultExecutor) updateConnectionSettings(ctx context.Context, op operationParameters, hostname, instance string) error {
	host, err := op.hpManager.GetHost(op.location, hostname)
	if err != nil {
		return err
	}

	err = deployments.SetInstanceCapabilityAttribute(ctx, op.deploymentID, op.nodeName,
		instance, tosca.ComputeNodeEndpointCapabilityName, tosca.EndpointCapabilityIPAddressAttribute,
		host.Connection.Host)
	if err != nil {
		return err
	}
	credentials := types.Credential{
		User:  host.Connection.User,
		Token: host.Connection.Password,
		Keys: map[string]string{
			// 0 is the default key name we are trying to remove this by allowing multiple keys
			"0": host.Connection.PrivateKey,
		},
	}

	var credentialsMap map[string]interface{}
	err = mapstructure.Decode(credentials, &credentialsMap)
	if err != nil {
		return err
	}

	err = deployments.SetInstanceCapabilityAttributeComplex(ctx, op.deploymentID,
		op.nodeName, instance, tosca.ComputeNodeEndpointCapabilityName, "credentials", credentialsMap)
	if err != nil {
		return err
	}

	privateAddress, ok := host.Labels[tosca.ComputeNodePrivateAddressAttributeName]
	if !ok {
		privateAddress = host.Connection.Host
		events.WithContextOptionalFields(ctx).
			NewLogEntry(events.LogLevelWARN, op.deploymentID).Registerf(
			`no "%q label for host %q, will use the address from the connection section`,
			tosca.ComputeNodePrivateAddressAttributeName, hostname)
	}
	err = setInstanceAttributesValue(ctx, op, instance, privateAddress,
		[]string{tosca.EndpointCapabilityIPAddressAttribute, tosca.ComputeNodePrivateAddressAttributeName})
	if err != nil {
		return err
	}

	if publicAddress, ok := host.Labels[tosca.ComputeNodePublicAddressAttributeName]; ok {
		// For compatibility with components referencing a host public_ip_address,
		// defining an attribute public_ip_address as well
		err = setInstanceAttributesValue(ctx, op, instance, publicAddress,
			[]string{tosca.ComputeNodePublicAddressAttributeName, "public_ip_address"})
		if err != nil {
			return err
		}
	}

	if host.Connection.Port != 0 {
		err = deployments.SetInstanceCapabilityAttribute(ctx, op.deploymentID, op.nodeName,
			instance, tosca.ComputeNodeEndpointCapabilityName, tosca.EndpointCapabilityPortProperty,
			strconv.FormatUint(host.Connection.Port, 10))
		if err != nil {
			return err
		}
	}

	return setInstanceAttributesFromLabels(ctx, op, instance, host.Labels)
}

func (e *defaultExecutor) getAllocatedResourcesFromHostCapabilities(ctx context.Context, deploymentID, nodeName string) (map[string]string, error) {
	res := make(map[string]string, 0)
	p, err := deployments.GetCapabilityPropertyValue(ctx, deploymentID, nodeName, "host", "num_cpus")
	if err != nil {
		return nil, err
	}
	if p != nil && p.RawString() != "" {
		res["host.num_cpus"] = p.RawString()
	}

	p, err = deployments.GetCapabilityPropertyValue(ctx, deploymentID, nodeName, "host", "mem_size")
	if err != nil {
		return nil, err
	}
	if p != nil && p.RawString() != "" {
		res["host.mem_size"] = p.RawString()
	}

	p, err = deployments.GetCapabilityPropertyValue(ctx, deploymentID, nodeName, "host", "disk_size")
	if err != nil {
		return nil, err
	}
	if p != nil && p.RawString() != "" {
		res["host.disk_size"] = p.RawString()
	}
	return res, nil
}

// this allows to retrieve generic resources from extended host capability
func (e *defaultExecutor) getGenericResourcesFromHostCapabilities(ctx context.Context, deploymentID, nodeName string) ([]*GenericResource, error) {
	genericResourcesValue, err := deployments.GetCapabilityPropertyValue(ctx, deploymentID, nodeName, hostCapabilityName, genericResourcesPropertyName)
	if err != nil {
		return nil, err
	}
	if genericResourcesValue == nil || genericResourcesValue.RawString() == "" {
		return nil, err
	}
	list, ok := genericResourcesValue.Value.([]interface{})
	if !ok {
		return nil, errors.New("failed to retrieve generic resources: not expected type")
	}
	gResources := make([]*GenericResource, 0)
	for _, item := range list {
		gResource, err := toGenericResource(item)
		if err != nil {
			return nil, err
		}
		gResources = append(gResources, gResource)
	}
	return gResources, err
}

func (e *defaultExecutor) hostsPoolDelete(originalCtx context.Context, cc *api.Client,
	cfg config.Configuration, op operationParameters, allocatedResources map[string]string) error {
	instances, err := tasks.GetInstances(originalCtx, op.taskID, op.deploymentID, op.nodeName)
	if err != nil {
		return err
	}
	var errs error
	for _, instance := range instances {
		ctx := events.AddLogOptionalFields(originalCtx, events.LogOptionalFields{events.InstanceID: instance})
		hostname, err := deployments.GetInstanceAttributeValue(originalCtx, op.deploymentID, op.nodeName, instance, "hostname")
		if err != nil {
			errs = multierror.Append(errs, err)
		}
		if hostname == nil || hostname.RawString() == "" {
			events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelWARN, op.deploymentID).Registerf(
				"instance %q of node %q has no registered hostname. This may be due to an error at creation time.",
				instance, op.nodeName)
			continue
		}
		allocation, err := op.hpManager.Release(op.location, hostname.RawString(), op.deploymentID, op.nodeName, instance)
		if err != nil {
			errs = multierror.Append(errs, err)
		}
		err = op.hpManager.UpdateResourcesLabels(op.location, hostname.RawString(), allocatedResources, add, updateResourcesLabels, allocation.GenericResources, addElements, updateGenericResourcesLabels)
		if err != nil {
			errs = multierror.Append(errs, err)
		}
	}
	return errors.Wrap(errs, "errors encountered during hosts pool node release. Some hosts maybe not properly released.")
}
